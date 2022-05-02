/*
 * Copyright (c) 2017, MegaEase
 * All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package requestadaptor

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"github.com/megaease/easegress/pkg/context"
	"github.com/megaease/easegress/pkg/filters"
	"github.com/megaease/easegress/pkg/logger"
	"github.com/megaease/easegress/pkg/protocols/httpprot"
	"github.com/megaease/easegress/pkg/protocols/httpprot/httpheader"
	"github.com/megaease/easegress/pkg/util/pathadaptor"
	"github.com/stretchr/testify/assert"
)

func init() {
	logger.InitNop()
}

func setRequest(t *testing.T, ctx *context.Context, id string, req *http.Request) {
	httpreq, err := httpprot.NewRequest(req)
	assert.Nil(t, err)
	_, err = httpreq.FetchPayload()
	assert.Nil(t, err)
	ctx.SetRequest(id, httpreq)
	ctx.UseRequest(id, id)
}

func defaultFilterSpec(spec *Spec) filters.Spec {
	spec.BaseSpec.MetaSpec.Kind = Kind
	spec.BaseSpec.MetaSpec.Name = "request-adaptor"
	result, _ := filters.NewSpec(nil, "pipeline-demo", spec)
	return result
}

func getGzipEncoding(t *testing.T, data []byte) io.Reader {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	defer gw.Close()
	_, err := gw.Write(data)
	assert.Nil(t, err)
	return &buf
}

func TestRequestAdaptor(t *testing.T) {
	assert := assert.New(t)
	{
		// normal case
		spec := defaultFilterSpec(&Spec{})
		ra := kind.CreateInstance(spec)
		ra.Init()
		assert.Equal(Kind, ra.Kind().Name)
		assert.Nil(ra.Status())

		newRA := kind.CreateInstance(spec)
		newRA.Inherit(ra)
		newRA.Close()
	}

	{
		// panic when invalid compress type
		spec := defaultFilterSpec(&Spec{Compress: "zip"})
		ra := kind.CreateInstance(spec)
		assert.Panics(func() { ra.Init() })
	}

	{
		// panic when invalid decompress type
		spec := defaultFilterSpec(&Spec{Decompress: "zip"})
		ra := kind.CreateInstance(spec)
		assert.Panics(func() { ra.Init() })
	}

	{
		// panic when set both set compress and decompress
		spec := defaultFilterSpec(&Spec{
			Decompress: "gzip",
			Compress:   "gzip",
		})
		ra := kind.CreateInstance(spec)
		assert.Panics(func() { ra.Init() })
	}

	{
		// panic when set body and Decompress
		spec := defaultFilterSpec(&Spec{
			Decompress: "gzip",
			Body:       "body",
		})
		ra := kind.CreateInstance(spec)
		assert.Panics(func() { ra.Init() })
	}
}

func TestDecompress(t *testing.T) {
	assert := assert.New(t)

	{
		// decompress without body in spec
		spec := defaultFilterSpec(&Spec{
			Decompress: "gzip",
		})

		ra := kind.CreateInstance(spec)
		ra.Init()

		{
			// compress success
			data := "123"
			req, err := http.NewRequest(http.MethodPost, "127.0.0.1", getGzipEncoding(t, []byte(data)))
			assert.Nil(err)
			req.Header.Add("Content-Encoding", "gzip")

			ctx := context.New(nil)
			setRequest(t, ctx, "req1", req)

			ans := ra.Handle(ctx)
			assert.Equal("", ans)

			encoding := ctx.Request().Header().Get("Content-Encoding")
			assert.Equal("", encoding)

			body, err := io.ReadAll(ctx.Request().GetPayload())
			assert.Nil(err)
			assert.Equal("123", string(body))
		}

		{
			// compress fail
			data := "123"
			req, err := http.NewRequest(http.MethodPost, "127.0.0.1", bytes.NewReader([]byte(data)))
			assert.Nil(err)
			req.Header.Add("Content-Encoding", "gzip")

			ctx := context.New(nil)
			setRequest(t, ctx, "req1", req)

			ans := ra.Handle(ctx)
			assert.Equal(resultDecompressFail, ans)
			ctx.Finish()
		}
	}
}

func TestCompress(t *testing.T) {
	assert := assert.New(t)

	{
		// compress without body in spec
		spec := defaultFilterSpec(&Spec{
			Compress: "gzip",
		})
		ra := kind.CreateInstance(spec)
		ra.Init()

		data := "123"
		req, err := http.NewRequest(http.MethodPost, "127.0.0.1", bytes.NewReader([]byte(data)))
		assert.Nil(err)

		ctx := context.New(nil)
		setRequest(t, ctx, "req1", req)

		ans := ra.Handle(ctx)
		assert.Equal("", ans)

		encoding := ctx.Request().Header().Get("Content-Encoding")
		assert.Equal("gzip", encoding)

		reader, err := gzip.NewReader(ctx.Request().GetPayload())
		assert.Nil(err)
		defer reader.Close()
		body, err := io.ReadAll(reader)
		assert.Nil(err)
		assert.Equal("123", string(body))
	}

	{
		// compress with body in spec
		spec := defaultFilterSpec(&Spec{
			Body:     "spec_body",
			Compress: "gzip",
		})
		ra := kind.CreateInstance(spec)
		ra.Init()

		{
			// original gzip body
			data := "123"
			req, err := http.NewRequest(http.MethodPost, "127.0.0.1", getGzipEncoding(t, []byte(data)))
			assert.Nil(err)
			req.Header.Add("Content-Encoding", "gzip")

			ctx := context.New(nil)
			setRequest(t, ctx, "req1", req)

			ans := ra.Handle(ctx)
			assert.Equal("", ans)

			encoding := ctx.Request().Header().Get("Content-Encoding")
			assert.Equal("gzip", encoding)

			reader, err := gzip.NewReader(ctx.Request().GetPayload())
			assert.Nil(err)
			defer reader.Close()
			body, err := io.ReadAll(reader)
			assert.Nil(err)
			assert.Equal("spec_body", string(body))
		}

		{
			// original uncompressed body
			data := "123"
			req, err := http.NewRequest(http.MethodPost, "127.0.0.1", bytes.NewReader([]byte(data)))
			assert.Nil(err)

			ctx := context.New(nil)
			setRequest(t, ctx, "req1", req)

			ans := ra.Handle(ctx)
			assert.Equal("", ans)
			ctx.Finish()

			encoding := ctx.Request().Header().Get("Content-Encoding")
			assert.Equal("gzip", encoding)

			reader, err := gzip.NewReader(ctx.Request().GetPayload())
			assert.Nil(err)
			defer reader.Close()
			body, err := io.ReadAll(reader)
			assert.Nil(err)
			assert.Equal("spec_body", string(body))
		}
	}
}

func TestHandle(t *testing.T) {
	assert := assert.New(t)

	spec := defaultFilterSpec(&Spec{
		Method: http.MethodDelete,
		Host:   "127.0.0.2",
		Body:   "123",
		Header: &httpheader.AdaptSpec{
			Add: map[string]string{"X-Add": "add-value"},
			Set: map[string]string{"X-Set": "set-value"},
		},
		Path: &pathadaptor.Spec{
			Replace: "/path",
		},
	})
	ra := kind.CreateInstance(spec)
	ra.Init()

	req, err := http.NewRequest(http.MethodPost, "127.0.0.1", nil)
	assert.Nil(err)

	ctx := context.New(nil)
	setRequest(t, ctx, "req1", req)

	ans := ra.Handle(ctx)
	assert.Equal("", ans)
	ctx.Finish()

	httpreq := ctx.Request().(*httpprot.Request)
	method := httpreq.Method()
	assert.Equal(http.MethodDelete, method)

	host := httpreq.Host()
	assert.Equal("127.0.0.2", host)

	body, err := io.ReadAll(httpreq.GetPayload())
	assert.Nil(err)
	assert.Equal("123", string(body))

	headerValue := httpreq.Std().Header.Get("X-Add")
	assert.Equal("add-value", headerValue)

	headerValue = httpreq.Std().Header.Get("X-Set")
	assert.Equal("set-value", headerValue)

	path := httpreq.Path()
	assert.Equal("/path", path)
}
