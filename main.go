package main

import (
	"context"
	"fmt"
	fatallog "log"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	resourceUsageDebug = false
)

func createResourceRequirements(cpuRequest, memoryRequest, cpuLimit, memoryLimit string) *corev1.ResourceRequirements {

	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpuRequest),
			corev1.ResourceMemory: resource.MustParse(memoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpuLimit),
			corev1.ResourceMemory: resource.MustParse(memoryLimit),
		},
	}

	return &resources
}

func main() {

	ctx := context.Background()

	kLog := log.FromContext(ctx)

	myClient, err := createMyClient()
	if err != nil {
		fatallog.Fatal(err)
	}

	{

		// resources := createResourceRequirements("250m", "250Mi", "2", "2Gi")

		experimentsToRun := []experiment{

			createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "500Mi")}),

			createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "700Mi")}),

			createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}),

			createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "1100Mi")}),

			// createExperiment_mediumApps(),
			// createExperiment_216KiBConfigMap(),
			// createExperiment_unmanagedConfigMaps(),
		}

		for _, experiment := range experimentsToRun {

			actionOutput("------------------------------ " + experiment.name + " -------------------------------------")

			actionOutput("Pre: Delete previous test resources")
			if err := deleteOldAnnotatedResources(ctx, myClient); err != nil {
				fatallog.Fatal(err)
			}

			actionOutput("Pre: Setup environment")

			if experiment.appControllerSettings != nil {
				actionOutput("Application Controller settings:")
				actionOutput(fmt.Sprintf("- operationProcessors: %d", experiment.appControllerSettings.operationProcessors))
				actionOutput(fmt.Sprintf("- statusProcessors: %d", experiment.appControllerSettings.statusProcessors))
				actionOutput(fmt.Sprintf("- kubectlParallelismLimit: %d", experiment.appControllerSettings.kubectlParallelismLimit))

				if experiment.appControllerSettings.resourceRequirements != nil {
					actionOutput(fmt.Sprintf("- resources: %v", experiment.appControllerSettings.resourceRequirements))
				}

			} else {
				actionOutput("Application Controller settings: default")
			}

			if err := initialConfiguration(ctx, experiment.appControllerSettings, myClient, kLog); err != nil {
				fatallog.Fatal(err)
			}

			_, err := beginExperiment(ctx, experiment, myClient, kLog)

			actionOutput("Post: Cleaning up old resources")
			myClient.ledger.disposeAll(ctx, myClient)

			if err != nil {
				fatallog.Fatal(err)
			}

			// if !success {
			// 	actionOutput("Exit due to failed experiment")
			// 	os.Exit(1)
			// }

		}

	}
}

func runExperiment(ctx context.Context, experiment experiment, myClient *myClient, kLog logr.Logger) (bool, error) {

	actionOutput("Pre: Delete previous test resources")
	if err := deleteOldAnnotatedResources(ctx, myClient); err != nil {
		return false, err
	}

	actionOutput("Pre: Setup environment")

	if experiment.appControllerSettings != nil {
		actionOutput("Application Controller settings:")
		actionOutput(fmt.Sprintf("- operationProcessors: %d", experiment.appControllerSettings.operationProcessors))
		actionOutput(fmt.Sprintf("- statusProcessors: %d", experiment.appControllerSettings.statusProcessors))
		actionOutput(fmt.Sprintf("- kubectlParallelismLimit: %d", experiment.appControllerSettings.kubectlParallelismLimit))

		if experiment.appControllerSettings.resourceRequirements != nil {
			actionOutput(fmt.Sprintf("- resources: %v", experiment.appControllerSettings.resourceRequirements))
		}

	} else {
		actionOutput("Application Controller settings: default")
	}

	if err := initialConfiguration(ctx, experiment.appControllerSettings, myClient, kLog); err != nil {
		return false, err
	}

	success, err := beginExperiment(ctx, experiment, myClient, kLog)

	actionOutput("Post: Cleaning up old resources")
	myClient.ledger.disposeAll(ctx, myClient)

	return success, err

}
