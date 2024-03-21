package main

import (
	"context"
	"fmt"
	"time"

	appv1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/argoproj/gitops-engine/pkg/health"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func createExperiment_216KiBConfigMap() experiment {
	fn := func(ctx context.Context, expClient *experimentClient, cancelSignalled chan string, kLog logr.Logger) (*experimentResult, error) {

		totalTasks := 250

		baseMemoryUsage, err := collectBaseMemoryUsage()
		if err != nil {
			return nil, err
		}

		cancelled := false
		{
			var availableWork []runnableTask
			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experiment216KiBConfigMap{
					deleteOnly: false,
				})
			}

			actionOutput("creating ConfigMap Applications")
			var err error
			if cancelled, err = runTasksConcurrently(ctx, 5, availableWork, cancelSignalled, expClient); err != nil {
				return nil, err
			}
		}

		var success bool
		if !cancelled {
			lowest, highest, avg, err := collectPostMemoryUsage()
			if err != nil {
				return nil, err
			}

			// adjust values to remove the base memory usage
			lowest = lowest - baseMemoryUsage
			highest = highest - baseMemoryUsage
			avg = avg - baseMemoryUsage

			outputStatus("")
			outputStatus("Base memory usage:", baseMemoryUsage)
			outputStatus("Lowest memory usage (3 mins, adjusted):", lowest)
			outputStatus("Highest memory usage (3 mins, adjusted):", highest)
			outputStatus("Avg memory usage (3 mins, adjusted):", avg)

			success = highest > 900 && highest <= 1300
		} else {
			success = false
		}

		actionOutput("Deleting ConfigMap Applications and Namespaces")
		{

			var availableWork []runnableTask

			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experiment216KiBConfigMap{
					deleteOnly: true,
				})
			}

			if _, err := runTasksConcurrently(ctx, 30, availableWork, nil, expClient); err != nil {
				return nil, err
			}

		}

		return &experimentResult{
			success: success,
		}, nil
	}

	return experiment{
		name: "216KiBConfigMap",
		fn:   fn,
	}
}

var _ runnableTask = &workTask_experiment216KiBConfigMap{}

type workTask_experiment216KiBConfigMap struct {
	deleteOnly bool
}

func (wt *workTask_experiment216KiBConfigMap) runTask(ctx context.Context, taskNumber int, c *experimentClient) error {

	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("jgw-%v", taskNumber),
		},
	}
	app := appv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fmt.Sprintf("jgw-%v", taskNumber),
			Namespace:  ArgoCDNamespace,
			Finalizers: []string{
				// "finalizer.argocd.argoproj.io/background",
				// "resources-finalizer.argocd.argoproj.io",
			},
		},
		Spec: appv1.ApplicationSpec{
			Source: &appv1.ApplicationSource{
				RepoURL: "https://github.com/jgwest/repo-template",
				Path:    "config-map-216KiB/",
			},
			Destination: appv1.ApplicationDestination{
				Name:      "argo-cd-secret",
				Namespace: namespace.Name,
			},
			Project: "default",
			SyncPolicy: &appv1.SyncPolicy{
				Automated: &appv1.SyncPolicyAutomated{
					Prune:      true,
					SelfHeal:   true,
					AllowEmpty: false,
				},
				Retry: &appv1.RetryStrategy{
					Limit: 10000,
				},
			},
		},
	}

	if wt.deleteOnly {
		if err := deleteResource(ctx, &app, c); err != nil {
			return err
		}
		if err := deleteResource(ctx, &namespace, c); err != nil {
			return err
		}
		return nil
	}

	if err := deleteAndCreateResource(ctx, &namespace, c); err != nil {
		return err
	}

	if err := deleteAndCreateResource(ctx, &app, c); err != nil {
		return err
	}

	expectedSyncStatus := appv1.SyncStatusCodeSynced
	if err := waitForArgoCDApplicationSyncStatusAndHealth(ctx, app, &expectedSyncStatus, nil, c); err != nil {
		return err
	}

	return nil
}

// Create many ConfigMaps that are NOT managed by an Argo CD Application, and verify that application-controller memory usage does NOT (significantly) increase.
// application-controller memory usage should only (significantly) increase while watching resources it has deployed.
func createExperiment_unmanagedConfigMaps() experiment {

	fn := func(ctx context.Context, c *experimentClient, cancelSignalled chan string, kLog logr.Logger) (*experimentResult, error) {

		var cmNamespace corev1.Namespace
		{
			actionOutput("Create destination Namespace for ConfigMaps")
			cmNamespace = corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cm",
				},
			}

			if err := deleteAndCreateResource(ctx, &cmNamespace, c); err != nil {
				return nil, err
			}

		}

		baseMemoryUsage, err := collectBaseMemoryUsage()
		if err != nil {
			return nil, err
		}

		actionOutput("Create 5000 ConfigMaps, each of size 128KiB")

		var availableWork []runnableTask

		for x := 0; x < 5000; x++ {
			availableWork = append(availableWork, &workTask_experiment_unmanagedConfigMaps{
				namespace: cmNamespace.Name,
			})
		}

		var cancelled bool
		if cancelled, err = runTasksConcurrently(ctx, 30, availableWork, cancelSignalled, c); err != nil {
			return nil, err
		}

		if !cancelled {
			actionOutput("ConfigMaps created, waiting for memory usage to stabilize")
			time.Sleep(30 * time.Second)

			memoryUsage, err := getAppControllerMemoryUsage()
			if err != nil {
				return nil, err
			}

			actionOutput(fmt.Sprintf("base memory usage: %v", baseMemoryUsage))
			actionOutput(fmt.Sprintf("memory usage: %v", memoryUsage))

			// Memory usage should not have increased more than 50%
			if float64(memoryUsage)/float64(baseMemoryUsage) < 1.5 {
				return &experimentResult{success: true}, nil
			} else {
				return &experimentResult{success: false}, nil
			}
		} else {
			return &experimentResult{success: false}, nil
		}
	}

	return experiment{
		name: "unmanagedConfigMaps",
		fn:   fn,
	}
}

func (wt *workTask_experiment_unmanagedConfigMaps) runTask(ctx context.Context, taskNumber int, c *experimentClient) error {

	dataMap := map[string]string{}

	for y := 0; y < 128; y++ {
		dataMap[randomString(32)] = randomString(1000)
	}

	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cm-" + randomString(32),
			Namespace: wt.namespace,
		},
		Data: dataMap,
	}
	if err := createResource(ctx, &configMap, c); err != nil {
		return err
	}

	return nil
}

var _ runnableTask = &workTask_experiment_unmanagedConfigMaps{}

type workTask_experiment_unmanagedConfigMaps struct {
	namespace string
}

func createExperiment_largeApps(params experimentExecutionParams) experiment {

	fn := createExperimentFunction("apps/large-no-replicas", params.appsToTest, 0 /*1300*/, 99000)

	return experiment{
		name:                  fmt.Sprintf("Large apps - %d large apps", params.appsToTest),
		fn:                    fn,
		appControllerSettings: &params.appControllerSettings,
		runXTimes:             params.runXTimes,
	}
}

// TODO: wait for a certain CPU usage? (double digits?)
// TODO: should I wait for healthy on these? It would perhaps be more accurate/consistent.

func createExperiment_mediumApps() experiment {
	fn := createExperimentFunction("apps/medium-no-replicas", 200, 0 /* 900 */, 2000)

	return experiment{
		name: "Medium apps",
		fn:   fn,
	}
}

func createExperimentFunction(repoPath string, totalTasks int, memoryUsageLowerBound int, memoryUsageUpperBound int) experimentFunction {

	// experiment name
	// total tasks
	// expected memory usage range
	fn := func(ctx context.Context, c *experimentClient, cancelSignalled chan string, kLog logr.Logger) (*experimentResult, error) {

		baseMemoryUsage, err := collectBaseMemoryUsage()
		if err != nil {
			return nil, err
		}

		cancelled := false
		{
			var availableWork []runnableTask
			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experimentLargeApps{
					repoPath:   repoPath,
					deleteOnly: false,
				})
			}

			actionOutput("Creating Argo CD Applications, and waiting for sync")
			var err error
			if cancelled, err = runTasksConcurrently(ctx, totalTasks, availableWork, cancelSignalled, c); err != nil {
				return nil, err
			}
		}

		var success bool

		if !cancelled {
			if success, err := waitForAllHealthyAndSyncedAppsAndNoOOMIncrease(ctx, c); err != nil {
				return nil, err
			} else if !success {
				return &experimentResult{
					success: false,
				}, nil
			}

			{
				lowest, highest, avg, err := collectPostMemoryUsage()
				if err != nil {
					return nil, err
				}

				// adjust values to remove the base memory usage
				lowest = lowest - baseMemoryUsage
				highest = highest - baseMemoryUsage
				avg = avg - baseMemoryUsage

				outputStatus("")
				outputStatus("Base memory usage:", baseMemoryUsage)
				outputStatus("Lowest memory usage (3 mins, adjusted):", lowest)
				outputStatus("Highest memory usage (3 mins, adjusted):", highest)
				outputStatus("Avg memory usage (3 mins, adjusted):", avg)

				success = highest > memoryUsageLowerBound && highest <= memoryUsageUpperBound
			}

		}

		actionOutput("Cleaning up experiment resources")
		{
			var availableWork []runnableTask

			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experimentLargeApps{
					deleteOnly: true,
				})
			}

			if _, err := runTasksConcurrently(ctx, 20, availableWork, nil, c); err != nil {
				return nil, err
			}

		}
		return &experimentResult{
			success: success,
		}, nil
	}

	return fn
}

var _ runnableTask = &workTask_experimentLargeApps{}

type workTask_experimentLargeApps struct {
	repoPath   string
	deleteOnly bool
}

func (wt *workTask_experimentLargeApps) runTask(ctx context.Context, taskNumber int, c *experimentClient) error {

	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("jgw-%v", taskNumber),
		},
	}

	app := appv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fmt.Sprintf("jgw-%v", taskNumber),
			Namespace:  ArgoCDNamespace,
			Finalizers: []string{
				// "finalizer.argocd.argoproj.io/background",
				// "resources-finalizer.argocd.argoproj.io",
			},
		},
		Spec: appv1.ApplicationSpec{
			Source: &appv1.ApplicationSource{
				RepoURL: "https://github.com/jgwest/repo-template",
				Path:    wt.repoPath,
			},
			Destination: appv1.ApplicationDestination{
				Name:      "argo-cd-secret",
				Namespace: namespace.Name,
			},
			Project: "default",
			SyncPolicy: &appv1.SyncPolicy{
				Automated: &appv1.SyncPolicyAutomated{
					Prune:      true,
					SelfHeal:   true,
					AllowEmpty: false,
				},
				Retry: &appv1.RetryStrategy{
					Limit: 10000,
				},
			},
		},
	}

	if wt.deleteOnly {
		if err := deleteResource(ctx, &app, c); err != nil {
			return err
		}
		if err := deleteResource(ctx, &namespace, c); err != nil {
			return err
		}
		return nil
	}

	if err := deleteAndCreateResource(ctx, &namespace, c); err != nil {
		return err
	}

	if err := deleteAndCreateResource(ctx, &app, c); err != nil {
		return err
	}

	expectedSyncStatus := appv1.SyncStatusCodeSynced
	expectedHealthStatus := health.HealthStatusHealthy

	if err := waitForArgoCDApplicationSyncStatusAndHealth(ctx, app, &expectedSyncStatus, &expectedHealthStatus, c); err != nil {
		return err
	}

	return nil
}
