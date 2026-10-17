package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type serviceClient struct {
	base      *url.URL
	http      *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
	token     string
	localOnly bool
}
type apiError struct {
	Code    int
	Message string
}

func (e *apiError) Error() string   { return e.Message }
func transportError(err error) bool { var api *apiError; return err != nil && !errors.As(err, &api) }
func newServiceClient(address string, credentials ...string) (*serviceClient, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("Invalid backend URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("Backend URL must be an http(s) origin without credentials, path, query or fragment")
	}
	u.Path = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Scheme == "http" {
		hostname := u.Hostname()
		if hostname == "localhost" {
			port := u.Port()
			u.Host = "127.0.0.1"
			if port != "" {
				u.Host = net.JoinHostPort("127.0.0.1", port)
			}
		} else if ip := net.ParseIP(hostname); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("Plain HTTP is allowed only for loopback; use HTTPS or SSH forwarding")
		}
	}
	token := ""
	if len(credentials) > 0 {
		token = strings.TrimSpace(credentials[0])
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &serviceClient{base: u, http: &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("Backend redirects are not allowed") }}, ctx: ctx, cancel: cancel, token: token}, nil
}
func (c *serviceClient) close() { c.cancel(); c.http.CloseIdleConnections() }
func (c *serviceClient) endpoint(path, instance string) *url.URL {
	u := *c.base
	u.Path = path
	q := url.Values{"instance": {instance}}
	u.RawQuery = q.Encode()
	return &u
}
func (c *serviceClient) jsonCall(path, instance string, input, output any) error {
	method := http.MethodGet
	var body io.Reader
	timeout := 3 * time.Second
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("Invalid request")
		}
		body = bytes.NewReader(data)
		method = http.MethodPost
		timeout = 75 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, instance).String(), body)
	if err != nil {
		return fmt.Errorf("Invalid backend request")
	}
	c.authorize(req)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Backend unavailable")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("Backend response interrupted")
	}
	if res.StatusCode != 200 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error == "" {
			problem.Error = "Backend request rejected"
		}
		return &apiError{res.StatusCode, problem.Error}
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return fmt.Errorf("Invalid backend response")
	}
	return nil
}
func (c *serviceClient) authorize(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.localOnly {
		req.Header.Set("X-Prototype-Local-Only", "1")
	}
}
func (c *serviceClient) call(path, instance string, r *request) (instanceState, error) {
	var input any
	if r != nil {
		input = r
	}
	var state instanceState
	if err := c.jsonCall(path, instance, input, &state); err != nil {
		return instanceState{}, err
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
