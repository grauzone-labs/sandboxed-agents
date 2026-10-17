package main

import "sync"

type instanceInfo struct {
	Status       string `json:"status"`
	ServiceID    string `json:"serviceID,omitempty"`
	Error        string `json:"error,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Address      string `json:"address"`
	Group        string `json:"group"`
	SandboxCount int    `json:"sandboxCount"`
	ImageCount   int    `json:"imageCount"`
	SeedCount    int    `json:"seedCount"`
}

type fakeFleet struct {
	mu              sync.Mutex
	auth            *authVault
	ownerCredential string
	remotes         map[string]*remoteConnection
	remoteOrder     []string
	hosts           map[string]*fakeHost
	instances       []instanceInfo
	terminals       *terminalHub
	service         serviceInfo
}

type instanceState struct {
	Tokens []tokenInfo  `json:"tokens"`
	Origin *serviceInfo `json:"origin,omitempty"`
	fakeState
	Instance  instanceInfo   `json:"instance"`
	Instances []instanceInfo `json:"instances"`
	Service   serviceInfo    `json:"service"`
}

func newFakeFleet(local *fakeHost) *fakeFleet {
	auth, owner := newAuthVault()
	return &fakeFleet{hosts: map[string]*fakeHost{"local-fake": local}, instances: []instanceInfo{
		{ID: "local-fake", Name: "Local workstation", Kind: "Local / simulated inventory", Address: "This machine", Group: "default", SeedCount: 5, Status: "connected"},
	}, terminals: newTerminalHub(), service: newServiceInfo(), auth: auth, ownerCredential: owner, remotes: map[string]*remoteConnection{}}
}
