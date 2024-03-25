package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	appv1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/argoproj/gitops-engine/pkg/health"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
	yaml "sigs.k8s.io/yaml"
)

type experimentClient struct {
	ledger             *resourceCreationLedger
	kClient            client.Client
	argoCDClient       dynamic.NamespaceableResourceInterface
	subscriptionClient dynamic.NamespaceableResourceInterface
	csvClient          dynamic.NamespaceableResourceInterface
	ipClient           dynamic.NamespaceableResourceInterface
}

type resourceCreationLedger struct {
	mutex            sync.Mutex
	resourcesCreated []client.Object
}

func (ledger *resourceCreationLedger) addObject(obj client.Object) {
	ledger.mutex.Lock()
	defer ledger.mutex.Unlock()

	ledger.resourcesCreated = append(ledger.resourcesCreated, obj)
}

func (ledger *resourceCreationLedger) disposeAll(ctx context.Context, c *experimentClient) error {
	ledger.mutex.Lock()
	defer ledger.mutex.Unlock()

	actionOutput("Deleting test Namespaces")
	// 1) Delete namespaces, first
	{
		var workToComplete []runnableTask
		for _, obj := range ledger.resourcesCreated {

			if _, isNamespace := obj.(*corev1.Namespace); !isNamespace {
				continue
			}

			workToComplete = append(workToComplete, &workTask_disposeOfClientObject{
				obj: obj,
			})
		}
		if _, err := runTasksConcurrently(ctx, 50, workToComplete, nil, c); err != nil {
			return err
		}
	}

	actionOutput("Deleting other test resources")
	// 2) Delete everything else, second
	{
		var workToComplete []runnableTask
		for _, obj := range ledger.resourcesCreated {

			if _, isNamespace := obj.(*corev1.Namespace); isNamespace {
				continue
			}

			workToComplete = append(workToComplete, &workTask_disposeOfClientObject{
				obj: obj,
			})

		}
		if _, err := runTasksConcurrently(ctx, 50, workToComplete, nil, c); err != nil {
			return err
		}
	}

	ledger.resourcesCreated = []client.Object{}

	return nil
}

var _ runnableTask = &workTask_disposeOfClientObject{}

type workTask_disposeOfClientObject struct {
	obj client.Object
}

func (wt *workTask_disposeOfClientObject) runTask(ctx context.Context, taskNumber int, c *experimentClient) error {
	if err := deleteResource(ctx, wt.obj, c); err != nil {
		if !apierr.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func deleteAndCreateResource(ctx context.Context, objParam client.Object, c *experimentClient) error {

	obj := (objParam.DeepCopyObject()).(client.Object)

	if err := deleteResource(ctx, obj, c); err != nil {
		return err
	}

	return createResource(ctx, objParam, c)

}

const (
	ResourceUsageAnnotation = "resource-usage-resource"
)

func deleteOldAnnotatedResources(ctx context.Context, c *experimentClient) error {

	var argocdApplicationList appv1.ApplicationList
	if err := c.kClient.List(ctx, &argocdApplicationList); err != nil {
		if strings.Contains(err.Error(), "no matches for kind") {
			return nil
		}
		return err
	} else {

		for _, app := range argocdApplicationList.Items {

			if _, exists := app.Annotations[ResourceUsageAnnotation]; exists {

				if err := deleteResource(ctx, &app, c); err != nil {
					return err
				}
			}
		}
	}

	var workToComplete []runnableTask

	var namespaceList corev1.NamespaceList
	if err := c.kClient.List(ctx, &namespaceList); err != nil {
		return err
	}

	for i := range namespaceList.Items {
		namespace := namespaceList.Items[i]

		if _, exists := namespace.Annotations[ResourceUsageAnnotation]; exists {

			workToComplete = append(workToComplete, &workTask_disposeOfClientObject{
				obj: &namespace,
			})

		}
	}

	if _, err := runTasksConcurrently(ctx, 50, workToComplete, nil, c); err != nil {
		return err
	}

	return nil
}

func createResource(ctx context.Context, objParam client.Object, c *experimentClient) error {

	annots := objParam.GetAnnotations()
	if annots == nil {
		annots = map[string]string{}
	}
	annots[ResourceUsageAnnotation] = "true"
	objParam.SetAnnotations(annots)

	if err := c.kClient.Create(ctx, objParam); err != nil {
		return err
	}
	c.ledger.addObject(objParam)

	return nil
}

func createExperimentClient() (*experimentClient, error) {
	restConfig, err := generateRestConfig()
	if err != nil {
		return nil, err
	}

	kClient, err := createClient()
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &experimentClient{
		ledger:             &resourceCreationLedger{},
		kClient:            kClient,
		argoCDClient:       dynamicClient.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1beta1", Resource: "argocds"}),
		subscriptionClient: dynamicClient.Resource(schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}),
		csvClient:          dynamicClient.Resource(schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}),
		ipClient:           dynamicClient.Resource(schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "installplans"}),
	}, nil
}

func actionOutput(str string) {
	fmt.Println(dateTimeString(), "| * "+str)
}

func debugActionOutput(str string) {
	if resourceUsageDebug {
		fmt.Println("* " + str)
	}
}

func getPodResourceUsage(podName string, podNamespace string) (map[string][]string, error) {
	stdout, stderr, err := runCommand("kubectl", "top", "pod", podName, "-n", podNamespace)
	if err != nil {
		fmt.Println(stderr)
		return nil, err
	}

	res := map[string][]string{}

	for _, line := range strings.Split(stdout, "\n") {

		if strings.Contains(line, "MEMORY(bytes)") {
			continue
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected number of fields: %s", line)
		}

		res[fields[0]] = []string{fields[1], fields[2]}
	}

	return res, nil
}

func dynamicCreateInNamespace(ctx context.Context, yamlVal string, namespace string, c dynamic.NamespaceableResourceInterface) error {
	res := map[string]any{}
	if err := yaml.Unmarshal([]byte(yamlVal), &res); err != nil {
		return err
	}
	_, err := c.Namespace(namespace).Create(ctx, &unstructured.Unstructured{
		Object: res,
	}, metav1.CreateOptions{})

	if err != nil {
		return fmt.Errorf("unable to dynamic create in NS %w", err)
	}

	return nil

}

func deleteResource(ctx context.Context, objParam client.Object, c *experimentClient) error {

	obj := (objParam.DeepCopyObject()).(client.Object)

	if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {

		if !apierr.IsNotFound(err) {
			return err
		}

	} else {
		if err := c.kClient.Delete(ctx, obj); err != nil {
			if !apierr.IsNotFound(err) {
				return err
			}
		}

		for {
			if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err == nil {
				time.Sleep(1000 * time.Millisecond)
				debugActionOutput("Waiting for deletion of " + obj.GetName() + " " + reflect.TypeOf(obj).String())
			} else {
				break
			}
		}
	}

	return nil

}

func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyz")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}

func printObject(obj client.Object) {

	bytes, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(bytes))

	// return string(bytes)

}

func runCommand(cmdList ...string) (string, string, error) {

	cmd := exec.Command(cmdList[0], cmdList[1:]...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err

}

func collectBaseMemoryUsage() (int, error) {

	actionOutput("Waiting 60s to collect base memory usage")
	time.Sleep(60 * time.Second)

	memoryUsage, err := getAppControllerMemoryUsage()
	if err != nil {
		return 0, err
	}
	return memoryUsage, nil

}

func waitForAllHealthyAndSyncedAppsAndNoOOMIncrease(ctx context.Context, c *experimentClient) (bool, error) {

	const (
		mustSucceedWithinXMinutes = 10
		successOnNoOOMForXMinutes = 3
	)

	failExpireTime := time.Now().Add(time.Minute * mustSucceedWithinXMinutes)

	lastRestartCountSeen := 0

	successExpireTime := time.Now().Add(time.Minute * successOnNoOOMForXMinutes)

	actionOutput(fmt.Sprintf("Waiting for Argo CD Application controller to not restart for %d minutes", successOnNoOOMForXMinutes))
	for {

		if time.Now().After(failExpireTime) {
			actionOutput(fmt.Sprintf("Application controller never stabilized. Final restart count was: %v", lastRestartCountSeen))
			return false, nil // fail!
		}

		if time.Now().After(successExpireTime) {
			break
		}

		applicationControllerPod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "argocd-application-controller-0", Namespace: ArgoCDNamespace}}

		if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(&applicationControllerPod), &applicationControllerPod); err != nil {
			return false, err
		}

		if len(applicationControllerPod.Status.ContainerStatuses) != 1 {
			return false, fmt.Errorf("unexpected number of container statuses")
		}

		container := applicationControllerPod.Status.ContainerStatuses[0]

		// Every time the restart increases, increase the wait time
		if container.RestartCount != int32(lastRestartCountSeen) {
			actionOutput(fmt.Sprintf("- Restart seen in application-controller: %v", container.RestartCount))
			lastRestartCountSeen = int(container.RestartCount)
			// now := time.Now()
			// lastOOMIncreaseSeen = &now
			successExpireTime = time.Now().Add(time.Minute * successOnNoOOMForXMinutes) // reset the wait counter
		}

		time.Sleep(10 * time.Second)

	}

	actionOutput("Waiting for all Argo CD Application to be synced/healthy")
	for {

		if time.Now().After(failExpireTime) {
			actionOutput("Application controller never stabilized. Argo CD Applications existed that were unhealty or out of sync")

			return false, nil // fail!
		}

		var argoCDApplicationList appv1.ApplicationList

		if err := c.kClient.List(ctx, &argoCDApplicationList, &client.ListOptions{Namespace: ArgoCDNamespace}); err != nil {
			return false, err
		}

		ready := true
		for _, app := range argoCDApplicationList.Items {

			if app.Status.Sync.Status != appv1.SyncStatusCodeSynced {
				actionOutput("- App is not synced: " + app.Name)
				ready = false
			}

			if app.Status.Health.Status != health.HealthStatusHealthy {
				actionOutput("- App is not healthy: " + app.Name)
				ready = false
			}

		}
		if ready {
			break
		} else {
			time.Sleep(5 * time.Second)
		}
	}

	return true, nil

}

func collectPostMemoryUsage() (int, int, int, error) {

	actionOutput("Waiting 30s for controller memory usage to stabilize")
	time.Sleep(30 * time.Second)

	actionOutput("Gathering memory usage statistics for the next 3 minutes")
	expireTime := time.Now().Add(3 * time.Minute)

	var (
		highestMemUsageSeen = 0
		lowestMemUsageSeen  = math.MaxInt
		valuesSeen          = []int{}
	)

	for {

		if time.Now().After(expireTime) {
			break
		}

		memoryUsage, err := getAppControllerMemoryUsage()
		if err != nil {
			return 0, 0, 0, err
		}

		if memoryUsage > highestMemUsageSeen {
			highestMemUsageSeen = memoryUsage
		}

		if memoryUsage < lowestMemUsageSeen {
			lowestMemUsageSeen = memoryUsage
		}

		valuesSeen = append(valuesSeen, memoryUsage)

		time.Sleep(1 * time.Second)
	}

	averageMemorySeen := 0
	for _, value := range valuesSeen {
		averageMemorySeen += value
	}
	averageMemorySeen = averageMemorySeen / len(valuesSeen)

	return lowestMemUsageSeen, highestMemUsageSeen, averageMemorySeen, nil

}

func getAppControllerMemoryUsage() (int, error) {
	containers, err := getPodResourceUsage("argocd-application-controller-0", ArgoCDNamespace)
	if err != nil {
		return 0, err
	}

	memory := containers["argocd-application-controller-0"][1]
	memoryInt, err := strconv.Atoi(strings.ReplaceAll(memory, "Mi", ""))
	if err != nil {
		return 0, err
	}

	return memoryInt, nil

}

type experimentResult struct {
	success bool // success is true if experiment has expected result, false otherwise.
}

func waitForArgoCDApplicationSyncStatusAndHealth(ctx context.Context, app appv1.Application, expectedSyncStatus *appv1.SyncStatusCode, expectedHealthStatus *health.HealthStatusCode, c *experimentClient) error {

	requiredConditions := 0

	if expectedHealthStatus != nil {
		requiredConditions++
	}

	if expectedSyncStatus != nil {
		requiredConditions++
	}

	if requiredConditions == 0 {
		return fmt.Errorf("invalid parameters")
	}

	for {
		if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(&app), &app); err != nil {
			return err
		}

		if resourceUsageDebug {
			fmt.Println(app.Name, app.Status.Health.Status, app.Status.Sync.Status)
		}

		conditionsMet := 0

		if expectedSyncStatus != nil && app.Status.Sync.Status == *expectedSyncStatus {
			conditionsMet++
		}

		if expectedHealthStatus != nil && app.Status.Health.Status == *expectedHealthStatus {
			conditionsMet++
		}

		if conditionsMet == requiredConditions {
			break
		}

		time.Sleep(1000 * time.Millisecond)
	}

	return nil
}

type experimentFunction func(ctx context.Context, expClient *experimentClient, oomDetected chan string, kLog logr.Logger) (*experimentResult, error)

type experiment struct {
	name                  string
	fn                    experimentFunction
	appControllerSettings *applicationControllerSettings

	// runXTimes: run the test X (e.g. 3) times, and fail (fast) if it fails at least once
	runXTimes int
}

func beginExperiment(ctx context.Context, e experiment, expClient *experimentClient, kLog logr.Logger) (bool, error) {

	expireTime := time.Now().Add(time.Minute * 30)
	// cancelCancelChan, cancelChan := createChannelSignalOnTimeout(ctx, expireTime, expClient)

	cancelCancelChan, cancelChan := createChannelSignalOnOOM(ctx, expClient)

	fmt.Println()
	actionOutput("Beginning experiment: " + e.name)
	result, err := e.fn(ctx, expClient, cancelChan, kLog)
	if err != nil {
		return false, err
	}

	go func() {
		cancelCancelChan <- true // TODO: Hmm
	}()

	if restartedContainer, err := lookForPodRestarts(ctx, expClient); err != nil {
		return false, err
	} else if restartedContainer != "" {
		actionOutput("ERROR: Container restart detected, likely due to OOM: " + restartedContainer)
		result.success = false
	}

	if time.Now().After(expireTime) {
		actionOutput("FAIL: Experiment expired.")
		result.success = false
	}

	if result.success {
		outputStatus("SUCCESS")
	} else {
		outputStatus("FAIL")
	}
	actionOutput("Experiment complete")

	return result.success, nil
}

func createChannelSignalOnTimeout(expireTime time.Time) (chan bool, chan string) {

	cancelCancelChan := make(chan bool)
	cancelChan := make(chan string)

	// expireTime := time.Now().Add(time.Minute * time.Duration(expireTimeInMinutes))

	go func() {
		var expired bool
	outer:
		for {
			select {
			case <-cancelCancelChan:
				expired = false
				break outer
			default:
			}

			if time.Now().After(expireTime) {
				expired = true
				break outer
			}

			time.Sleep(time.Second * 1)
		}

		if expired {
			actionOutput("Signalling test expiration")

			for {

				// select {
				// case <-cancelCancelChan:
				// 	expired = false
				// 	break outer
				// default:
				// }

				cancelChan <- "true"
			}

		}

	}()

	return cancelCancelChan, cancelChan
}

func createChannelSignalOnOOM(ctx context.Context, c *experimentClient) (chan bool, chan string) {

	// Sending a message to cancelCancelChan will terminate the cancel thread
	cancelCancelChan := make(chan bool)

	// createChannelSignalOnOOM will send a string (the name of the container that restarted) to cancelChan, when a pod restart is detected.
	cancelChan := make(chan string)

	go func() {
		var err error
		var restartedContainer string

	outer:
		for {

			// When a value is received on 'cancelCancelChan', then exist.
			select {
			case <-cancelCancelChan:
				break outer
			default:
			}

			if restartedContainer, err = lookForPodRestarts(ctx, c); err == nil && restartedContainer != "" {
				actionOutput("ERROR: Container restart detected, likely due to OOM: " + restartedContainer)
				break outer
			}
			time.Sleep(5 * time.Second)
		}
		for {
			cancelChan <- restartedContainer
		}
	}()

	return cancelCancelChan, cancelChan
}

type runnableTask interface {
	runTask(ctx context.Context, taskNumber int, client *experimentClient) error
}

func runTasksConcurrently(ctx context.Context, maxConcurrentTasks int, availableWork []runnableTask, signalCancelled chan string, expClient *experimentClient) (bool, error) {

	ctx, cancelFunc := context.WithCancel(ctx)

	concurrentTasks := 0

	totalWork := len(availableWork)

	var mutex sync.Mutex

	var nextTaskNumber int

	const (
		reportEveryXPercent = 5
		reportEveryXSeconds = 60
	)

	nextReportTime := time.Now().Add(time.Second * reportEveryXSeconds)
	nextPercentToReport := 100 - reportEveryXPercent

	cancelSignalled := false // If cancel is signalled, then stop adding new tasks, and wait for active tasks to complete
	for {

		workAdded := false

		breakOutOfForLoop := func() bool {
			mutex.Lock()
			defer mutex.Unlock()

			{
				report := false
				percentRemaining := int(float32(100) * float32(len(availableWork)+concurrentTasks) / float32(totalWork))

				if percentRemaining > 0 && percentRemaining <= nextPercentToReport {
					nextPercentToReport = percentRemaining - reportEveryXPercent
					nextPercentToReport = nextPercentToReport - (nextPercentToReport % reportEveryXPercent)
					if nextPercentToReport < 0 {
						nextPercentToReport = 0
					}
					// nextPercentToReport = (percentRemaining - (percentRemaining % reportEveryXPercent)) - reportEveryXPercent
					// nextPercentToReport -= reportEveryXPercent
					report = true
				}

				if time.Now().After(nextReportTime) {
					report = true
				}

				if report {
					nextReportTime = time.Now().Add(time.Second * reportEveryXSeconds)
					actionOutput(fmt.Sprintf("- Tasks remaining: %d%%, %d/%d", percentRemaining, len(availableWork)+concurrentTasks, totalWork))
				}
			}

			if len(availableWork) == 0 && concurrentTasks == 0 {
				return true
			}

			if concurrentTasks == 0 && cancelSignalled {
				return true
			}

			if signalCancelled != nil {
				select {
				case <-signalCancelled:
					cancelSignalled = true
					cancelFunc()
				default:
				}
			}

			if concurrentTasks < maxConcurrentTasks && len(availableWork) > 0 && !cancelSignalled {

				concurrentTasks++
				localNextTaskNumber := nextTaskNumber
				nextTaskNumber++

				work := availableWork[0]
				availableWork = availableWork[1:]

				workAdded = true
				go func(taskNumber int) {

					work.runTask(ctx, taskNumber, expClient)

					mutex.Lock()
					defer mutex.Unlock()
					concurrentTasks--

				}(localNextTaskNumber)
			}

			return false
		}()

		if breakOutOfForLoop {
			break
		}

		if !workAdded {
			time.Sleep(100 * time.Millisecond)
		}

	}

	return cancelSignalled, nil

}

// lookForPodRestarts returns the name of any containers that have >0 restarts, or "" otherwise
func lookForPodRestarts(ctx context.Context, c *experimentClient) (string, error) {

	var podList corev1.PodList

	if err := c.kClient.List(ctx, &podList, &client.ListOptions{Namespace: ArgoCDNamespace}); err != nil {
		return "", err
	}

	for _, pod := range podList.Items {

		for _, containerStatus := range pod.Status.ContainerStatuses {

			if strings.Contains(containerStatus.Name, "dex") { // When running some versions of upstream Argo CD manifests on OpenShift, dex will perm fail to start, so we just ignore it (since these tests don't use dex).
				continue
			}

			if containerStatus.RestartCount > 0 {

				oomKilled := false
				if containerStatus.LastTerminationState.Terminated != nil && containerStatus.LastTerminationState.Terminated.Reason == "OOMKilled" {
					oomKilled = true
				}

				if !oomKilled {

					outputStatus("ERROR: container restart was detected, but OOMKilled was not found. Possible the container was restarted for another reason?")

					printObject(&pod)

					// podYaml, err := yaml.Marshal(pod)
					// if err != nil {
					// 	fmt.Println("on restart, unable to unmarshal pod yaml", err)
					// } else {
					// 	fmt.Println("on restart:", string(podYaml))
					// }
				}

				return containerStatus.Name, nil
			}
		}
	}

	return "", nil

}

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
