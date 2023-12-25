package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	yaml "sigs.k8s.io/yaml"

	// networkingv1 "k8s.io/api/networking/v1"
	// rbacv1 "k8s.io/api/rbac/v1"

	fatallog "log"
)

func main() {

	workingDir, err := os.Getwd()
	if err != nil {
		fatallog.Fatal("unable to get working dir", err)
	}

	templateDir := filepath.Join(workingDir, "template")

	fmt.Println("hi")

	// small

	smallInput := repoGenInput{
		numberOfApps:     3,
		replicasPerApp:   1,
		configMapsPerApp: 2,
		servicesPerApp:   1,
	}

	generateRepoNew(smallInput, templateDir, "/tmp")

}

type repoGenInput struct {
	numberOfApps int

	replicasPerApp   int
	configMapsPerApp int
	servicesPerApp   int
}

func generateRepoNew(input repoGenInput, templateDir string, outputDir string) error {

	var generatedFiles []string

	startingServicePort := 16379
	nextServicePort := startingServicePort

	for appNum := 0; appNum < input.numberOfApps; appNum++ {

		appName := fmt.Sprintf("app-%d", appNum)

		// create configmaps
		for configmapNum := 0; configmapNum < input.configMapsPerApp; configmapNum++ {

			bytes, err := os.ReadFile(filepath.Join(templateDir, "configmap.yaml"))
			if err != nil {
				return err
			}
			configText := string(bytes)
			configText = strings.ReplaceAll(configText, "{{ app-name }}", appName)
			configText = strings.ReplaceAll(configText, "{{ configmap-name }}", fmt.Sprintf("configmap-%d", configmapNum))

			filename := fmt.Sprintf("configmap-%s-%d.yaml", appName, configmapNum)
			if err := os.WriteFile(filepath.Join(outputDir, filename), []byte(configText), 0600); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, filename)
		}

		// create service

		var servicePorts []int

		for serviceNum := 0; serviceNum < input.configMapsPerApp; serviceNum++ {

			currServicePort := nextServicePort
			nextServicePort++
			servicePorts = append(servicePorts, currServicePort)

			serviceName := fmt.Sprintf("service-%d", serviceNum)

			bytes, err := os.ReadFile(filepath.Join(templateDir, "service.yaml"))
			if err != nil {
				return err
			}
			serviceText := string(bytes)

			serviceText = strings.ReplaceAll(serviceText, "{{ app-name }}", appName)
			serviceText = strings.ReplaceAll(serviceText, "{{ service-name }}", serviceName)
			serviceText = strings.ReplaceAll(serviceText, "{{ service-port }}", fmt.Sprintf("%d", currServicePort))

			filename := fmt.Sprintf("service-%s-%d.yaml", appName, serviceNum)
			if err := os.WriteFile(filepath.Join(outputDir, filename), []byte(serviceText), 0600); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, filename)

		}

		// create deployment
		{
			bytes, err := os.ReadFile(filepath.Join(templateDir, "deployment.yaml"))
			if err != nil {
				return err
			}

			deplText := string(bytes)
			deplText = strings.ReplaceAll(deplText, "{{ app-name }}", appName)
			deplText = strings.ReplaceAll(deplText, "{{ app-replicas }}", fmt.Sprintf("%d", input.replicasPerApp))

			var portEntries string
			for _, servicePort := range servicePorts {
				portEntries += `
          - containerPort: ` + fmt.Sprintf("%d", servicePort) + `
            protocol: TCP`
			}

			deplText = strings.ReplaceAll(deplText, "{{ app-ports }}", portEntries)

			filename := fmt.Sprintf("deployment-%s.yaml", appName)
			if err := os.WriteFile(filepath.Join(outputDir, filename), []byte(deplText), 0600); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, filename)

		}
	}

	{
		kustomizeFile := `
kind: Kustomization
apiVersion: kustomize.config.k8s.io/v1beta1
resources:
`
		for _, generatedFile := range generatedFiles {
			kustomizeFile += "- " + generatedFile + "\n"
		}

		if err := os.WriteFile(filepath.Join(outputDir, "kustomization.yaml"), []byte(kustomizeFile), 0600); err != nil {
			return err
		}

	}

	return nil
}

func meow(kClient client.Client) error {

	var configMapList corev1.ConfigMapList

	if err := kClient.List(context.Background(), &configMapList, &client.ListOptions{Namespace: "jgw"}); err != nil {
		return err
	}

	{

		path := "/home/jgw/workspace/sourceblade/resource-usage/jgw"
		files, err := ioutil.ReadDir(path)
		if err != nil {
			return err
		}

		for _, file := range files {
			if file.IsDir() {
				continue
			}

			bytes, err := os.ReadFile(filepath.Join(path, file.Name()))
			if err != nil {
				return err
			}

			str := strings.ReplaceAll(string(bytes), "namespace: jgw", "")

			if err := os.WriteFile(filepath.Join(path, file.Name()), []byte(str), 0600); err != nil {
				return err
			}

			// fmt.Println(file.Name(), file.IsDir())
		}
	}

	for _, configMap := range configMapList.Items {

		if !strings.Contains(configMap.Name, "jgw") {
			continue
		}

		// fmt.Println("kubectl get configmap/" + configMap.Name + " -o yaml > " + configMap.Name + ".yaml")
	}

	// for y := 0; y < 50; y++ {

	// 	name := fmt.Sprintf("jgw-%v", strings.ToLower(RandomString(32)))

	// 	if err := generateRepo(name, "jgw", kClient); err != nil {
	// 		fmt.Println(err)
	// 		return
	// 	}

	// }

	return nil
}

func generateRepo(name string, namespace string, kClient client.Client) error {

	// depl := createDeployment()

	res := createConfigMap()

	res.Name = name
	res.Namespace = namespace

	if err := kClient.Create(context.Background(), &res); err != nil {
		return err
	}

	return nil

	// I can just tell argo cd to watch the cluster, then create the objects on it?
}

func createDeployment() appsv1.Deployment {

	zero := int32(0)

	expectedSize := 900 * 1024

	baseDeployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"k": "v"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"k": "v"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "my-container",
							Image: "quay.io/not-a-real-container/still-not",
							Args: []string{
								"arg",
							},
							Env: []corev1.EnvVar{
								// {
								// 	Name:  "env1",
								// 	Value: "val1",
								// },
							},
						},
					},
				},
			},
		},
	}

	for {
		bytes, err := yaml.Marshal(baseDeployment)
		if err != nil {
			panic(err)
		}

		if len(string(bytes)) >= expectedSize {
			break
		}

		envs := baseDeployment.Spec.Template.Spec.Containers[0].Env

		envs = append(envs, corev1.EnvVar{
			Name:  randomString(100),
			Value: randomString(10_000),
		})

		baseDeployment.Spec.Template.Spec.Containers[0].Env = envs

	}

	return baseDeployment
}

func createService() corev1.Service {

	trafficPolicy := corev1.ServiceInternalTrafficPolicyCluster

	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "service",
		},
		Spec: corev1.ServiceSpec{
			InternalTrafficPolicy: &trafficPolicy,
			Ports: []corev1.ServicePort{
				{
					Name:       "metrics",
					Protocol:   corev1.ProtocolTCP,
					Port:       8443,
					TargetPort: intstr.FromInt(8443),
				},
			},
		},
	}

	return service
}

func createConfigMap() corev1.ConfigMap {

	expectedSize := 200 * 1024

	baseConfigMap := corev1.ConfigMap{
		// ObjectMeta: metav1.ObjectMeta{
		// 	Name: "config-map",
		// },
		Data: map[string]string{},
	}

	for {
		bytes, err := yaml.Marshal(baseConfigMap)
		if err != nil {
			panic(err)
		}

		baseConfigMap.Data[randomString(32)] = randomString(10_000)

		if len(string(bytes)) >= expectedSize {
			break
		}
	}
	return baseConfigMap
}

func createSecret() corev1.Secret {

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "secret",
		},
		Data: map[string][]byte{},
	}

	return secret
}

func MarshallObj(obj client.Object) string {

	bytes, err := yaml.Marshal(&obj)
	if err != nil {
		panic("unable to marshal obj")
	}

	return string(bytes)

}

func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyz")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}
