// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import catalog "github.com/lyft/flyteplugins/go/tasks/pluginmachinery/catalog"
import context "context"
import mock "github.com/stretchr/testify/mock"

// AsyncClient is an autogenerated mock type for the AsyncClient type
type AsyncClient struct {
	mock.Mock
}

// Download provides a mock function with given fields: ctx, requests
func (_m *AsyncClient) Download(ctx context.Context, requests ...catalog.DownloadRequest) (catalog.DownloadFuture, error) {
	_va := make([]interface{}, len(requests))
	for _i := range requests {
		_va[_i] = requests[_i]
	}
	var _ca []interface{}
	_ca = append(_ca, ctx)
	_ca = append(_ca, _va...)
	ret := _m.Called(_ca...)

	var r0 catalog.DownloadFuture
	if rf, ok := ret.Get(0).(func(context.Context, ...catalog.DownloadRequest) catalog.DownloadFuture); ok {
		r0 = rf(ctx, requests...)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(catalog.DownloadFuture)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, ...catalog.DownloadRequest) error); ok {
		r1 = rf(ctx, requests...)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Upload provides a mock function with given fields: ctx, requests
func (_m *AsyncClient) Upload(ctx context.Context, requests ...catalog.UploadRequest) (catalog.UploadFuture, error) {
	_va := make([]interface{}, len(requests))
	for _i := range requests {
		_va[_i] = requests[_i]
	}
	var _ca []interface{}
	_ca = append(_ca, ctx)
	_ca = append(_ca, _va...)
	ret := _m.Called(_ca...)

	var r0 catalog.UploadFuture
	if rf, ok := ret.Get(0).(func(context.Context, ...catalog.UploadRequest) catalog.UploadFuture); ok {
		r0 = rf(ctx, requests...)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(catalog.UploadFuture)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, ...catalog.UploadRequest) error); ok {
		r1 = rf(ctx, requests...)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}