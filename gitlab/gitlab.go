package bosgitlab

import (
	"log"
	"sync"

	"github.com/xanzy/go-gitlab"
)

var (
	client *gitlab.Client
	once   sync.Once
	token  string
	url    string
)

// Initialize sets the GitLab token and URL for the client
func Initialize(tkn, baseURL string) {
	token = tkn
	url = baseURL
}

// GetClient returns the singleton GitLab client instance
func GetClient() *gitlab.Client {
	once.Do(func() {
		if token == "" || url == "" {
			log.Fatal("GitLab client requires initialization with token and URL")
		}

		var err error
		client, err = gitlab.NewClient(token, gitlab.WithBaseURL(url))
		if err != nil {
			log.Fatalf("Failed to create GitLab client: %v", err)
		}
	})
	return client
}
