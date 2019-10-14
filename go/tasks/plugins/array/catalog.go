package array

import (
	"context"
	arrayCore "github.com/lyft/flyteplugins/go/tasks/plugins/array/core"
	"strconv"

	"github.com/lyft/flyteplugins/go/tasks/errors"
	"github.com/lyft/flyteplugins/go/tasks/pluginmachinery/catalog"
	"github.com/lyft/flyteplugins/go/tasks/pluginmachinery/core"
	"github.com/lyft/flyteplugins/go/tasks/pluginmachinery/io"
	"github.com/lyft/flyteplugins/go/tasks/pluginmachinery/ioutils"
	"github.com/lyft/flytestdlib/bitarray"
	"github.com/lyft/flytestdlib/logger"
	"github.com/lyft/flytestdlib/storage"

	idlCore "github.com/lyft/flyteidl/gen/pb-go/flyteidl/core"
)

// Check if there are any previously cached tasks. If there are we will only submit an ArrayJob for the
// non-cached tasks. The ArrayJob is now a different size, and each task will get a new index location
// which is different than their original location. To find the original index we construct an indexLookup array.
// The subtask can find it's original index value in indexLookup[JOB_ARRAY_INDEX] where JOB_ARRAY_INDEX is an
// environment variable in the pod
func DetermineDiscoverability(ctx context.Context, tCtx core.TaskExecutionContext, state arrayCore.State) (arrayCore.State, error) {

	// Check that the taskTemplate is valid
	taskTemplate, err := tCtx.TaskReader().Read(ctx)
	if err != nil {
		return state, err
	} else if taskTemplate == nil {
		return state, errors.Errorf(errors.BadTaskSpecification, "Required value not set, taskTemplate is nil")
	}

	// Extract the custom plugin pb
	arrayJob, err := arrayCore.ToArrayJob(taskTemplate.GetCustom())
	if err != nil {
		return state, err
	} else if arrayJob == nil {
		return state, errors.Errorf(errors.BadTaskSpecification, "Could not extract custom array job")
	}

	// Save this in the state
	state = state.SetOriginalArraySize(arrayJob.Size)
	state = state.SetOriginalMinSuccesses(arrayJob.MinSuccesses)

	// If the task is not discoverable, then skip data catalog work and move directly to launch
	if taskTemplate.Metadata == nil || !taskTemplate.Metadata.Discoverable {
		logger.Infof(ctx, "Task is not discoverable, moving to launch phase...")
		// Set an empty indexes to cache. This task won't try to write to catalog anyway.
		state = state.SetIndexesToCache(bitarray.NewBitSet(uint(arrayJob.Size)))
		state = state.SetActualArraySize(int(arrayJob.Size))
		state = state.SetPhase(arrayCore.PhaseLaunch, core.DefaultPhaseVersion)
		return state, nil
	}

	// Otherwise, run the data catalog steps - create and submit work items to the catalog processor,
	// build input readers
	inputReaders, err := ConstructInputReaders(ctx, tCtx.DataStore(), tCtx.InputReader().GetInputPrefixPath(), int(arrayJob.Size))
	if err != nil {
		return state, err
	}

	// build output writers
	outputWriters, err := ConstructOutputWriters(ctx, tCtx.DataStore(), tCtx.OutputWriter().GetOutputPrefixPath(), int(arrayJob.Size))
	if err != nil {
		return state, err
	}

	// build work items from inputs and outputs
	workItems, err := ConstructCatalogReaderWorkItems(ctx, tCtx.TaskReader(), inputReaders, outputWriters)
	if err != nil {
		return state, err
	}

	// Check catalog, and if we have responses from catalog for everything, then move to writing the mapping file.
	future, err := tCtx.Catalog().Download(ctx, workItems...)
	if err != nil {
		return state, err
	}

	if future.GetResponseStatus() == catalog.ResponseStatusReady {
		resp, err := future.GetResponse()
		if err != nil {
			return state, err
		}

		// If all the sub-tasks are actually done, then we can just move on.
		if resp.GetCachedCount() == int(arrayJob.Size) {
			// TODO: This is not correct?  We still need to write parent level results?
			state.SetPhase(arrayCore.PhaseSuccess, core.DefaultPhaseVersion)
			return state, nil
		}

		indexLookup := CatalogBitsetToLiteralCollection(resp.GetCachedResults(), resp.GetResultsSize())
		// TODO: Is the right thing to use?  Haytham please take a look
		indexLookupPath, err := ioutils.GetIndexLookupPath(ctx, tCtx.DataStore(), tCtx.OutputWriter().GetOutputPrefixPath())
		if err != nil {
			return state, err
		}

		logger.Infof(ctx, "Writing indexlookup file to [%s], cached count [%d/%d], ",
			indexLookupPath, resp.GetCachedCount(), arrayJob.Size)
		err = tCtx.DataStore().WriteProtobuf(ctx, indexLookupPath, storage.Options{}, indexLookup)
		if err != nil {
			return state, err
		}

		state = state.SetIndexesToCache(arrayCore.InvertBitSet(resp.GetCachedResults()))
		state = state.SetPhase(arrayCore.PhaseLaunch, 0)
		state = state.SetActualArraySize(int(arrayJob.Size) - resp.GetCachedCount())
	} else {
		ownerSignal := tCtx.EnqueueOwner()
		future.OnReady(func(ctx context.Context, _ catalog.Future) {
			ownerSignal(ctx)
		})
	}

	return state, nil
}

func WriteToDiscovery(ctx context.Context, tCtx core.TaskExecutionContext, state arrayCore.State) (arrayCore.State, error) {

	// Check that the taskTemplate is valid
	taskTemplate, err := tCtx.TaskReader().Read(ctx)
	if err != nil {
		return state, err
	} else if taskTemplate == nil {
		return state, errors.Errorf(errors.BadTaskSpecification, "Required value not set, taskTemplate is nil")
	}

	// Extract the custom plugin pb
	arrayJob, err := arrayCore.ToArrayJob(taskTemplate.GetCustom())
	if err != nil {
		return state, err
	} else if arrayJob == nil {
		return state, errors.Errorf(errors.BadTaskSpecification, "Could not extract custom array job")
	}

	// input readers
	inputReaders, err := ConstructInputReaders(ctx, tCtx.DataStore(), tCtx.InputReader().GetInputPrefixPath(), int(arrayJob.Size))

	// output reader
	outputReaders, err := ConstructOutputReaders(ctx, tCtx.DataStore(), tCtx.OutputWriter().GetOutputPrefixPath(), int(arrayJob.Size))

	// Create catalog put items, but only put the ones that were not originally cached (as read from the catalog results bitset)
	catalogWriterItems, err := ConstructCatalogUploadRequests(*tCtx.TaskExecutionMetadata().GetTaskExecutionID().GetID().TaskId,
		tCtx.TaskExecutionMetadata().GetTaskExecutionID().GetID(), taskTemplate.Metadata.DiscoveryVersion,
		*taskTemplate.Interface, state.GetIndexesToCache(), inputReaders, outputReaders)

	if len(catalogWriterItems) == 0 {
		state.SetPhase(arrayCore.PhaseSuccess, core.DefaultPhaseVersion)
	}

	allWritten, err := WriteToCatalog(ctx, tCtx.EnqueueOwner(), tCtx.Catalog(), catalogWriterItems)
	if allWritten {
		state.SetPhase(arrayCore.PhaseSuccess, core.DefaultPhaseVersion)
	}

	return state, nil
}

func WriteToCatalog(ctx context.Context, ownerSignal core.SignalOwner, catalogClient catalog.AsyncClient,
	workItems []catalog.UploadRequest) (bool, error) {

	// Enqueue work items
	future, err := catalogClient.Upload(ctx, workItems...)
	if err != nil {
		return false, errors.Wrapf(arrayCore.ErrorWorkQueue, err,
			"Error enqueuing work items")
	}

	// Immediately read back from the work queue, and see if it's done.
	if future.GetResponseStatus() == catalog.ResponseStatusReady {
		return true, nil
	}

	future.OnReady(func(ctx context.Context, _ catalog.Future) {
		ownerSignal(ctx)
	})

	return false, nil
}

func ConstructCatalogUploadRequests(keyId idlCore.Identifier, taskExecId idlCore.TaskExecutionIdentifier,
	cacheVersion string, taskInterface idlCore.TypedInterface, whichTasksToCache *bitarray.BitSet,
	inputReaders []io.InputReader, outputReaders []io.OutputReader) ([]catalog.UploadRequest, error) {

	writerWorkItems := make([]catalog.UploadRequest, 0, len(inputReaders))

	if len(inputReaders) != len(outputReaders) {
		return nil, errors.Errorf(arrayCore.ErrorInternalMismatch, "Length different building catalog writer items %d %d",
			len(inputReaders), len(outputReaders))
	}

	for idx, input := range inputReaders {
		if !whichTasksToCache.IsSet(uint(idx)) {
			continue
		}

		wi := catalog.UploadRequest{
			Key: catalog.Key{
				Identifier:     keyId,
				InputReader:    input,
				CacheVersion:   cacheVersion,
				TypedInterface: taskInterface,
			},
			ArtifactData: outputReaders[idx],
			ArtifactMetadata: catalog.Metadata{
				TaskExecutionIdentifier: &taskExecId,
			},
		}

		writerWorkItems = append(writerWorkItems, wi)
	}

	return writerWorkItems, nil
}

func NewLiteralScalarOfInteger(number int64) *idlCore.Literal {
	return &idlCore.Literal{
		Value: &idlCore.Literal_Scalar{
			Scalar: &idlCore.Scalar{
				Value: &idlCore.Scalar_Primitive{
					Primitive: &idlCore.Primitive{
						Value: &idlCore.Primitive_Integer{
							Integer: number,
						},
					},
				},
			},
		},
	}
}

func CatalogBitsetToLiteralCollection(catalogResults *bitarray.BitSet, size int) *idlCore.LiteralCollection {
	literals := make([]*idlCore.Literal, 0, size)
	for i := 0; i < size; i++ {
		if !catalogResults.IsSet(uint(i)) {
			literals = append(literals, NewLiteralScalarOfInteger(int64(i)))
		}
	}
	return &idlCore.LiteralCollection{
		Literals: literals,
	}
}

func ConstructCatalogReaderWorkItems(ctx context.Context, taskReader core.TaskReader, inputs []io.InputReader,
	outputs []io.OutputWriter) ([]catalog.DownloadRequest, error) {

	t, err := taskReader.Read(ctx)
	if err != nil {
		return nil, err
	}

	workItems := make([]catalog.DownloadRequest, len(inputs))
	for idx, inputReader := range inputs {
		// TODO: Check if Id or Interface are empty and return err
		item := catalog.DownloadRequest{
			Key: catalog.Key{
				Identifier:     *t.Id,
				CacheVersion:   t.GetMetadata().DiscoveryVersion,
				InputReader:    inputReader,
				TypedInterface: *t.Interface,
			},
			Target: outputs[idx],
		}
		workItems = append(workItems, item)
	}

	return workItems, nil
}

func ConstructInputReaders(ctx context.Context, dataStore *storage.DataStore, inputPrefix storage.DataReference,
	size int) ([]io.InputReader, error) {

	inputReaders := make([]io.InputReader, 0, size)
	for i := 0; i < size; i++ {
		indexedInputLocation, err := dataStore.ConstructReference(ctx, inputPrefix, strconv.Itoa(i))
		if err != nil {
			return inputReaders, err
		}

		inputReader := ioutils.NewRemoteFileInputReader(ctx, dataStore, ioutils.NewInputFilePaths(ctx, dataStore, indexedInputLocation))
		inputReaders = append(inputReaders, inputReader)
	}

	return inputReaders, nil
}

func ConstructOutputWriters(ctx context.Context, dataStore *storage.DataStore, outputPrefix storage.DataReference,
	size int) ([]io.OutputWriter, error) {

	outputWriters := make([]io.OutputWriter, 0, size)

	for i := 0; i < size; i++ {
		dataReference, err := dataStore.ConstructReference(ctx, outputPrefix, strconv.Itoa(i))
		if err != nil {
			return outputWriters, err
		}

		writer := ioutils.NewRemoteFileOutputWriter(ctx, dataStore, ioutils.NewRemoteFileOutputPaths(ctx, dataStore, dataReference))
		outputWriters = append(outputWriters, writer)
	}

	return outputWriters, nil
}

func ConstructOutputReaders(ctx context.Context, dataStore *storage.DataStore, outputPrefix storage.DataReference,
	size int) ([]io.OutputReader, error) {

	outputReaders := make([]io.OutputReader, 0, size)

	for i := 0; i < size; i++ {
		dataReference, err := dataStore.ConstructReference(ctx, outputPrefix, strconv.Itoa(i))
		if err != nil {
			return outputReaders, err
		}

		outputPath := ioutils.NewRemoteFileOutputPaths(ctx, dataStore, dataReference)
		reader := ioutils.NewRemoteFileOutputReader(ctx, dataStore, outputPath, int64(999999999))
		outputReaders = append(outputReaders, reader)
	}

	return outputReaders, nil
}