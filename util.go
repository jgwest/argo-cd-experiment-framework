package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
	yaml "sigs.k8s.io/yaml"
)

type myClient struct {
	ledger             *resourceCreationLedger
	kClient            client.Client
	argoCDClient       dynamic.NamespaceableResourceInterface
	subscriptionClient dynamic.NamespaceableResourceInterface
	csvClient          dynamic.NamespaceableResourceInterface
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

func (ledger *resourceCreationLedger) disposeAll(ctx context.Context, c *myClient) error {
	ledger.mutex.Lock()
	defer ledger.mutex.Unlock()

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
		if err := runTasks(ctx, 50, workToComplete, c); err != nil {
			return err
		}
	}

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
		if err := runTasks(ctx, 50, workToComplete, c); err != nil {
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

func (wt *workTask_disposeOfClientObject) runTask(ctx context.Context, taskNumber int, c *myClient) error {
	if err := deleteResource(ctx, wt.obj, c); err != nil {
		if !apierr.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func deleteAndCreateResource(ctx context.Context, objParam client.Object, c *myClient) error {

	obj := (objParam.DeepCopyObject()).(client.Object)

	if err := deleteResource(ctx, obj, c); err != nil {
		return err
	}

	return createResource(ctx, objParam, c)

}

func createResource(ctx context.Context, objParam client.Object, c *myClient) error {

	if err := c.kClient.Create(context.Background(), objParam); err != nil {
		return err
	}
	c.ledger.addObject(objParam)

	return nil
}

func createMyClient() (*myClient, error) {
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

	return &myClient{
		ledger:             &resourceCreationLedger{},
		kClient:            kClient,
		argoCDClient:       dynamicClient.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1beta1", Resource: "argocds"}),
		subscriptionClient: dynamicClient.Resource(schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}),
		csvClient:          dynamicClient.Resource(schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}),
	}, nil
}

func actionOutput(str string) {
	fmt.Println("* " + str)
}

func debugActionOutput(str string) {
	if debug {
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

func deleteResource(ctx context.Context, objParam client.Object, c *myClient) error {

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
				debugActionOutput("Waiting for deletion of " + obj.GetName())
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

func print(obj client.Object) {

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

	actionOutput("Waiting to collect base memory usage")
	time.Sleep(30 * time.Second)

	memoryUsage, err := getAppControllerMemoryUsage()
	if err != nil {
		return 0, err
	}
	return memoryUsage, nil

}

func getAppControllerMemoryUsage() (int, error) {
	containers, err := getPodResourceUsage("argocd-application-controller-0", "argocd")
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
