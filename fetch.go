package cliche

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const maxRedirects = 10

type fetchOptions struct {
	timeout  time.Duration
	maxBytes int
	follow   bool
}

// fetch of Node returns a promise; here the response, since a script runs with no event loop.
func (e *engine) fetch(call goja.FunctionCall) goja.Value {
	if !goja.IsString(call.Argument(0)) {
		panic(e.vm.NewTypeError("fetch takes its url as a string"))
	}
	target, err := url.Parse(call.Argument(0).String())
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		panic(e.vm.NewTypeError(fmt.Sprintf("fetch takes an absolute http or https url, not %s",
			quote(call.Argument(0).String()))))
	}
	options := e.fetchOptions(call.Argument(1))
	ctx, cancel := context.WithTimeout(e.ctx, options.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		panic(e.vm.NewTypeError(err.Error()))
	}
	// Proxies of the environment are not taken: a script reads no environment. With no connection kept, the transport
	// has none to send the request again on.
	transport := &http.Transport{ForceAttemptHTTP2: true, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if !options.follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		panic(e.raise(e.fetchError(ctx, target, options.timeout, err)))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(options.maxBytes)+1))
	if err != nil {
		panic(e.raise(e.fetchError(ctx, target, options.timeout, err)))
	}
	if len(body) > options.maxBytes {
		panic(e.raise(e.fetchError(ctx, target, options.timeout,
			fmt.Errorf("the body is longer than maxBytes %d", options.maxBytes))))
	}
	return e.response(response, request, body)
}

func (e *engine) fetchOptions(value goja.Value) fetchOptions {
	const takes = "{ timeout, maxBytes, redirect, method? }"
	given, isObject := value.(*goja.Object)
	if !isObject || given.ClassName() != "Object" {
		panic(e.vm.NewTypeError("fetch takes its options as an object: " + takes))
	}
	for _, key := range given.Keys() {
		if !slices.Contains([]string{"timeout", "maxBytes", "redirect", "method"}, key) {
			panic(e.vm.NewTypeError(fmt.Sprintf("fetch takes no option %s: it takes %s", quote(key), takes)))
		}
	}
	if method := given.Get("method"); method != nil && !goja.IsUndefined(method) &&
		(!goja.IsString(method) || !strings.EqualFold(method.String(), http.MethodGet)) {
		panic(e.vm.NewTypeError("fetch sends only GET"))
	}
	timeout := e.fetchWhole(given, "timeout", 1)
	maxBytes := e.fetchWhole(given, "maxBytes", 0)
	redirect := given.Get("redirect")
	if redirect == nil || !goja.IsString(redirect) || (redirect.String() != "follow" && redirect.String() != "manual") {
		panic(e.vm.NewTypeError(`fetch takes the option redirect: "follow" or "manual"`))
	}
	return fetchOptions{timeout: time.Duration(timeout) * time.Millisecond, maxBytes: maxBytes,
		follow: redirect.String() == "follow"}
}

func (e *engine) fetchWhole(given *goja.Object, key string, least int) int {
	value := given.Get(key)
	if value == nil || goja.IsUndefined(value) {
		panic(e.vm.NewTypeError(fmt.Sprintf("fetch takes the option %s, which has no default", key)))
	}
	number := value.ToFloat()
	if !goja.IsNumber(value) || number != math.Trunc(number) || number < float64(least) || number > math.MaxInt32 {
		panic(e.vm.NewTypeError(fmt.Sprintf("fetch: the option %s is no whole number of 32 bits from %d", key,
			least)))
	}
	return int(number)
}

func (e *engine) fetchError(ctx context.Context, target *url.URL, timeout time.Duration, err error) *FetchError {
	reason := unwrapURL(err).Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && e.ctx.Err() == nil {
		reason = fmt.Sprintf("no whole response within the timeout of %d ms", timeout.Milliseconds())
	}
	return &FetchError{URL: target.Redacted(), Reason: reason}
}

func unwrapURL(err error) error {
	var failed *url.Error
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

func (e *engine) response(response *http.Response, request *http.Request, body []byte) *goja.Object {
	answer := e.vm.NewObject()
	e.set(answer, "status", response.StatusCode)
	e.set(answer, "ok", response.StatusCode >= 200 && response.StatusCode < 300)
	e.set(answer, "statusText", strings.TrimPrefix(response.Status, strconv.Itoa(response.StatusCode)+" "))
	e.set(answer, "url", response.Request.URL.String())
	e.set(answer, "redirected", response.Request != request)
	e.set(answer, "headers", e.headers(response.Header))
	e.set(answer, "text", func() string { return text(body) })
	e.set(answer, "json", func() goja.Value {
		parsed, err := e.parseJSON(goja.Undefined(), e.vm.ToValue(text(body)))
		if err != nil {
			panic(err)
		}
		return parsed
	})
	e.set(answer, "arrayBuffer", func() goja.ArrayBuffer { return e.vm.NewArrayBuffer(slices.Clone(body)) })
	return answer
}

// A body not in UTF-8 reads with U+FFFD in place of what does not decode, and without a BOM, as in fetch.
func text(body []byte) string {
	return strings.TrimPrefix(strings.ToValidUTF8(string(body), "\ufffd"), "\ufeff")
}

func (e *engine) headers(header http.Header) *goja.Object {
	names := make([]string, 0, len(header))
	joined := map[string]string{}
	for name, values := range header {
		lower := strings.ToLower(name)
		names = append(names, lower)
		joined[lower] = strings.Join(values, ", ")
	}
	slices.Sort(names)
	entries := func() [][2]string {
		pairs := make([][2]string, 0, len(names))
		for _, name := range names {
			pairs = append(pairs, [2]string{name, joined[name]})
		}
		return pairs
	}
	headers := e.vm.NewObject()
	e.set(headers, "get", func(name string) goja.Value {
		value, held := joined[strings.ToLower(name)]
		if !held {
			return goja.Null()
		}
		return e.vm.ToValue(value)
	})
	e.set(headers, "has", func(name string) bool {
		_, held := joined[strings.ToLower(name)]
		return held
	})
	e.set(headers, "getSetCookie", func() []string { return slices.Clone(header.Values("Set-Cookie")) })
	e.set(headers, "keys", func() []string { return slices.Clone(names) })
	e.set(headers, "values", func() []string {
		held := make([]string, 0, len(names))
		for _, name := range names {
			held = append(held, joined[name])
		}
		return held
	})
	e.set(headers, "entries", entries)
	e.set(headers, "forEach", func(each goja.Callable) {
		for _, pair := range entries() {
			if _, err := each(goja.Undefined(), e.vm.ToValue(pair[1]), e.vm.ToValue(pair[0]), headers); err != nil {
				panic(err)
			}
		}
	})
	return headers
}
