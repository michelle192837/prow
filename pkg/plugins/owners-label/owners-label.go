/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ownerslabel

import (
	"fmt"

	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/prow/pkg/config"
	"sigs.k8s.io/prow/pkg/github"
	"sigs.k8s.io/prow/pkg/pluginhelp"
	"sigs.k8s.io/prow/pkg/plugins"
)

const (
	// PluginName defines this plugin's registered name.
	PluginName = "owners-label"
)

func init() {
	plugins.RegisterPullRequestHandler(PluginName, handlePullRequest, helpProvider)
}

func helpProvider(config *plugins.Configuration, _ []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	return &pluginhelp.PluginHelp{
			Description: "The owners-label plugin automatically adds labels to PRs based on the files they touch. Specifically, the 'labels' sections of OWNERS files are used to determine which labels apply to the changes.",
		},
		nil
}

type ownersClient interface {
	FindLabelsForFile(path string) sets.Set[string]
}

type githubClient interface {
	AddLabel(org, repo string, number int, label string) error
	GetIssueLabels(org, repo string, number int) ([]github.Label, error)
	GetRepoLabels(owner, repo string) ([]github.Label, error)
	GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error)
}

func handlePullRequest(pc plugins.Agent, pre github.PullRequestEvent) error {
	if pre.Action != github.PullRequestActionOpened && pre.Action != github.PullRequestActionReopened && pre.Action != github.PullRequestActionSynchronize {
		return nil
	}

	oc, err := pc.OwnersClient.LoadRepoOwners(pre.Repo.Owner.Login, pre.Repo.Name, pre.PullRequest.Base.Ref)
	if err != nil {
		return fmt.Errorf("error loading RepoOwners: %w", err)
	}

	return handle(pc.GitHubClient, oc, pc.Logger, &pre)
}

func handle(ghc githubClient, oc ownersClient, log *logrus.Entry, pre *github.PullRequestEvent) error {
	org := pre.Repo.Owner.Login
	repo := pre.Repo.Name
	number := pre.Number

	// First see if there are any labels requested based on the files changed.
	changes, err := ghc.GetPullRequestChanges(org, repo, number)
	if err != nil {
		return fmt.Errorf("error getting PR changes: %w", err)
	}
	neededLabels := sets.New[string]()
	for _, change := range changes {
		neededLabels.Insert(sets.List(oc.FindLabelsForFile(change.Filename))...)
	}
	if neededLabels.Len() == 0 {
		// No labels requested for the given files. Return now to save API tokens.
		return nil
	}

	repoLabels, err := ghc.GetRepoLabels(org, repo)
	if err != nil {
		return err
	}
	issuelabels, err := ghc.GetIssueLabels(org, repo, number)
	if err != nil {
		return err
	}

	RepoLabelsExisting := sets.New[string]()
	for _, label := range repoLabels {
		RepoLabelsExisting.Insert(label.Name)
	}
	currentLabels := sets.New[string]()
	for _, label := range issuelabels {
		currentLabels.Insert(label.Name)
	}

	nonexistent := sets.New[string]()
	for _, labelToAdd := range sets.List(neededLabels.Difference(currentLabels)) {
		if !RepoLabelsExisting.Has(labelToAdd) {
			nonexistent.Insert(labelToAdd)
			continue
		}
		if err := ghc.AddLabel(org, repo, number, labelToAdd); err != nil {
			log.WithError(err).Errorf("GitHub failed to add the following label: %s", labelToAdd)
		}
	}

	if nonexistent.Len() > 0 {
		log.Warnf("Unable to add nonexistent labels: %q", sets.List(nonexistent))
	}
	return nil
}
