package collector

import (
	"fmt"
	bosgitlab "gitlab-exporter/gitlab"
	"log"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/xanzy/go-gitlab"
)

type repositoryCollector struct {
	logger *slog.Logger
}

func (c *repositoryCollector) Update(ch chan<- prometheus.Metric) error {
	client := bosgitlab.GetClient()
	const perPage = 50

	// Fetch the first page to get total page count
	_, resp, err := client.Projects.ListProjects(&gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{Page: 1, PerPage: perPage},
	})
	if err != nil {
		log.Fatalf("Failed to get initial project list: %v", err)
	}

	totalPages := resp.TotalPages
	allProjects := make([]*gitlab.Project, 0, resp.TotalItems)
	projectChannel := make(chan []*gitlab.Project, totalPages)
	wg := sync.WaitGroup{}

	// Fetch projects concurrently
	for page := 1; page <= totalPages; page++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			projects, _, err := client.Projects.ListProjects(&gitlab.ListProjectsOptions{
				Statistics:  gitlab.Bool(true),
				ListOptions: gitlab.ListOptions{Page: page, PerPage: perPage},
			})
			if err != nil {
				c.logger.Error(fmt.Sprintf("Failed to fetch page %d: %v", page, err))
				return
			}
			projectChannel <- projects
		}(page)
	}

	// Close the project channel after all goroutines complete
	go func() {
		wg.Wait()
		close(projectChannel)
	}()

	// Collect projects from all pages
	for projects := range projectChannel {
		allProjects = append(allProjects, projects...)
	}

	// Metric descriptors
	projectSizeDesc := prometheus.NewDesc("gitlab_project_size_bytes", "Size of the GitLab project repository in bytes", []string{"id", "name", "path"}, nil)
	createdMRDesc := prometheus.NewDesc("gitlab_project_created_merge_requests_last_hour_total", "Number of merge requests created in the last hour in the GitLab project", []string{"id", "name", "path"}, nil)
	mergedMRDesc := prometheus.NewDesc("gitlab_project_merged_merge_requests_last_hour_total", "Number of merge requests merged in the last hour in the GitLab project", []string{"id", "name", "path"}, nil)
	closedMRDesc := prometheus.NewDesc("gitlab_project_closed_merge_requests_last_hour_total", "Number of merge requests closed in the last hour in the GitLab project", []string{"id", "name", "path"}, nil)
	commitsDesc := prometheus.NewDesc("gitlab_project_commits_last_hour_total", "Number of commits pushed in the last hour in the GitLab project", []string{"id", "name", "path"}, nil)

	now := time.Now()
	lastHour := now.Add(-1 * time.Hour)

	// Processing each project concurrently
	for _, project := range allProjects {
		wg.Add(1)
		go func(project *gitlab.Project) {
			defer wg.Done()
			projectID := fmt.Sprintf("%d", project.ID)
			c.logger.Info(fmt.Sprintf("Processing Project: %s (ID: %d)", project.Name, project.ID))

			// Project size metric
			ch <- prometheus.MustNewConstMetric(
				projectSizeDesc,
				prometheus.GaugeValue,
				float64(project.Statistics.RepositorySize),
				projectID,
				project.Name,
				project.Path,
			)

			// Fetch MR counts and commits in parallel
			var createdMRs, mergedMRs, closedMRs, commits int
			mrWg := sync.WaitGroup{}
			mrWg.Add(2)

			// Fetch Merge Requests
			go func() {
				defer mrWg.Done()
				options := &gitlab.ListProjectMergeRequestsOptions{
					CreatedAfter: &lastHour,
					Scope:        gitlab.String("all"),
					ListOptions:  gitlab.ListOptions{PerPage: 100},
				}
				mrs, _, err := client.MergeRequests.ListProjectMergeRequests(projectID, options)
				if err != nil {
					c.logger.Error(fmt.Sprintf("Failed to fetch merge requests for project %s: %v", project.Name, err))
					return
				}

				for _, mr := range mrs {
					if mr.CreatedAt.After(lastHour) {
						createdMRs++
					}
					if mr.MergedAt != nil && mr.MergedAt.After(lastHour) {
						mergedMRs++
					}
					if mr.ClosedAt != nil && mr.ClosedAt.After(lastHour) {
						closedMRs++
					}
				}
			}()

			// Fetch Commits
			go func() {
				defer mrWg.Done()
				commitOptions := &gitlab.ListCommitsOptions{
					Since:       &lastHour,
					Until:       &now,
					ListOptions: gitlab.ListOptions{PerPage: 100},
				}
				commitList, _, err := client.Commits.ListCommits(projectID, commitOptions)
				if err != nil {
					c.logger.Error(fmt.Sprintf("Failed to fetch commits for project %s: %v", project.Name, err))
					return
				}
				commits = len(commitList)
			}()

			// Wait for both MR and commit fetching to complete
			mrWg.Wait()

			// Send metrics to Prometheus
			ch <- prometheus.MustNewConstMetric(createdMRDesc, prometheus.GaugeValue, float64(createdMRs), projectID, project.Name, project.Path)
			ch <- prometheus.MustNewConstMetric(mergedMRDesc, prometheus.GaugeValue, float64(mergedMRs), projectID, project.Name, project.Path)
			ch <- prometheus.MustNewConstMetric(closedMRDesc, prometheus.GaugeValue, float64(closedMRs), projectID, project.Name, project.Path)
			ch <- prometheus.MustNewConstMetric(commitsDesc, prometheus.GaugeValue, float64(commits), projectID, project.Name, project.Path)
		}(project)
	}

	// Wait for all project processing goroutines to complete
	wg.Wait()
	return nil
}

func init() {
	registerCollector("repository", true, NewRepositoryCollector)
}

func NewRepositoryCollector(logger *slog.Logger) (Collector, error) {
	return &repositoryCollector{logger: logger}, nil
}
