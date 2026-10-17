package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type serviceClient struct {
	base   *url.URL
	http   *http.Client
	ctx    context.Context
	cancel context.CancelFunc
}
type apiError struct {
	Code    int
	Message string
}

func (e *apiError) Error() string   { return e.Message }
func transportError(err error) bool { var api *apiError; return err != nil && !errors.As(err, &api) }
func newServiceClient(address string) (*serviceClient, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("Invalid backend URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("Backend URL must be an http(s) origin without credentials, path, query or fragment")
	}
	u.Path = ""
	ctx, cancel := context.WithCancel(context.Background())
	return &serviceClient{u, &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}, ctx, cancel}, nil
}
func (c *serviceClient) close() { c.cancel(); c.http.CloseIdleConnections() }
func (c *serviceClient) endpoint(path, instance string) *url.URL {
	u := *c.base
	u.Path = path
	q := url.Values{"instance": {instance}}
	u.RawQuery = q.Encode()
	return &u
}
func (c *serviceClient) call(path, instance string, r *request) (instanceState, error) {
	timeout := 3 * time.Second
	method := http.MethodGet
	var input io.Reader
	if r != nil {
		timeout = 75 * time.Second
		method = http.MethodPost
		data, _ := json.Marshal(r)
		input = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, instance).String(), input)
	if err != nil {
		return instanceState{}, err
	}
	req.Header.Set("Accept", "application/json")
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return instanceState{}, fmt.Errorf("Backend unavailable: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return instanceState{}, err
	}
	if res.StatusCode != 200 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error == "" {
			problem.Error = "Backend returned " + res.Status
		}
		return instanceState{}, &apiError{res.StatusCode, problem.Error}
	}
	var state instanceState
	if err = json.Unmarshal(data, &state); err != nil {
		return instanceState{}, fmt.Errorf("Invalid backend response")
	}
	if state.Service.ID == "" {
		return instanceState{}, fmt.Errorf("This backend is an older prototype; restart it with the updated binary")
	}
	return state, nil
}
func (c *serviceClient) snapshot(instance string) (instanceState, error) {
	return c.call("/api/state", instance, nil)
}
func (c *serviceClient) command(instance string, r request) (instanceState, error) {
	return c.call("/api/action", instance, &r)
}
func (c *serviceClient) review(instance string, r request) (instanceState, error) {
	return c.call("/api/action-review", instance, &r)
}
func (c *serviceClient) address() string { return strings.TrimSuffix(c.base.String(), "/") }
