package main

type ClusterConfiguration struct {
	ProviderName string `json:"type"`

	ProjectID   string
	Region      string
	Zone        string
	NumNodes    int
	MultiMaster bool
	MultiZone   bool
	Zones       []string
	ConfigFile  string
}
