package main

import (
	"context"
	"fmt"
	fatallog "log"
	"time"

	appv1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func createExperiment_216KiBConfigMap() experiment {
	fn := func(ctx context.Context, myClient *myClient, kLog logr.Logger) (*experimentResult, error) {

		baseMemoryUsage, err := collectBaseMemoryUsage()
		if err != nil {
			return nil, err
		}

		totalTasks := 250

		deleteOldResources := func() error {
			var availableWork []runnableTask

			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experiment216KiBConfigMap{
					deleteOnly: true,
				})
			}

			actionOutput("Deleting ConfigMap Applications and Namespaces")
			if err := runTasks(ctx, 30, availableWork, myClient); err != nil {
				return err
			}

			return nil
		}

		if err := deleteOldResources(); err != nil {
			return nil, err
		}

		{
			var availableWork []runnableTask
			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experiment216KiBConfigMap{
					deleteOnly: false,
				})
			}

			actionOutput("creating ConfigMap Applications")
			if err := runTasks(ctx, 5, availableWork, myClient); err != nil {
				return nil, err
			}
		}

		actionOutput("Waiting to collect post memory usage")
		time.Sleep(30 * time.Second)

		postMemoryUsage, err := getAppControllerMemoryUsage()
		if err != nil {
			return nil, err
		}

		// TODO: This sucess metric should subtract the base amount, first
		fmt.Println("Base memory usage:", baseMemoryUsage, "Memory usage:", postMemoryUsage)
		success := postMemoryUsage > 1200 && postMemoryUsage <= 1600

		actionOutput("Cleaning up experiment resources")

		if err := deleteOldResources(); err != nil {
			return nil, err
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

func (wt *workTask_experiment216KiBConfigMap) runTask(ctx context.Context, taskNumber int, c *myClient) error {

	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("jgw-%v", taskNumber),
		},
	}
	if wt.deleteOnly {
		if err := deleteResource(ctx, &namespace, c); err != nil {
			return err
		}
	} else {

		if err := deleteAndCreateResource(ctx, &namespace, c); err != nil {
			return err
		}

	}

	app := appv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fmt.Sprintf("jgw-%v", taskNumber),
			Namespace:  "argocd",
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
				// SyncOptions: appv1.SyncOptions{
				// 	"Replace=true",
				// },
			},
		},
	}

	if wt.deleteOnly {
		return deleteResource(ctx, &app, c)
	}

	if err := deleteAndCreateResource(ctx, &app, c); err != nil {
		return err
	}

	for {
		if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(&app), &app); err != nil {
			return err
		}

		if debug {
			fmt.Println(taskNumber, app.Name, app.Status.Health.Status, app.Status.Sync.Status)
		}

		if app.Status.Sync.Status == appv1.SyncStatusCodeSynced {
			break
		}

		time.Sleep(1000 * time.Millisecond)
	}

	return nil
}

// Create many ConfigMaps that are NOT managed by an Argo CD Application, and verify that application-controller memory usage does NOT (significantly) increase.
// application-controller memory usage should only (significantly) increase while watching resources it has deployed.
func createExperiment_unmanagedConfigMaps() experiment {

	fn := func(ctx context.Context, c *myClient, kLog logr.Logger) (*experimentResult, error) {

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

		if err := runTasks(ctx, 30, availableWork, c); err != nil {
			return nil, err
		}

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

	}

	return experiment{
		name: "unmanagedConfigMaps",
		fn:   fn,
	}
}

func (wt *workTask_experiment_unmanagedConfigMaps) runTask(ctx context.Context, taskNumber int, c *myClient) error {

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
		fatallog.Fatal(err)
	}

	return nil
}

var _ runnableTask = &workTask_experiment_unmanagedConfigMaps{}

type workTask_experiment_unmanagedConfigMaps struct {
	namespace string
}

type experimentResult struct {
	success bool // success is true if experiment has expected result, false otherwise.
}
