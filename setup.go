package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appv1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/argoproj/gitops-engine/pkg/health"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1 "k8s.io/api/apps/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	yaml "sigs.k8s.io/yaml"
)

// Code in this file from redhat-appstudio/managed-gitops repo

const (
	ArgoCDNamespace                           = "argocd"
	ArgoCDManagerServiceAccountPrefix         = "argocd-manager-"
	ArgoCDManagerClusterRoleNamePrefix        = "argocd-manager-cluster-role-"
	ArgoCDManagerClusterRoleBindingNamePrefix = "argocd-manager-cluster-role-binding-"
)

var (
	ArgoCDManagerNamespacePolicyRules = []rbacv1.PolicyRule{
		{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			Verbs:     []string{"*"},
		},
	}
)

func setupKustomizeArgoCD(ctx context.Context, applicationControllerSettingsParam *applicationControllerSettings, c *myClient) error {

	actionOutput("Deleting and recreating Argo CD namespace")
	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ArgoCDNamespace,
		},
	}
	if err := deleteAndCreateResource(ctx, &namespace, c); err != nil {
		return err
	}

	actionOutput("Creating Argo CD CRDs")

	execFolder, err := os.Getwd()
	if err != nil {
		return err
	}

	// execPath, err := os.Executable()
	// if err != nil {
	// 	return err
	// }
	// execFolder := filepath.Dir(execPath)

	stdout, stderr, err := runCommand("kubectl", "apply", "-k", "https://github.com/argoproj/argo-cd/manifests/crds?ref=v2.9.3")
	if err != nil {
		fmt.Println(stdout, stderr)
		return err
	}

	actionOutput("Generating Argo CD manifests")

	namespaceInstallYamlPath := filepath.Join(execFolder, "manifests", "argo", "namespace-install.yaml")

	if applicationControllerSettingsParam != nil {

		var newFileContents string
		bytes, err := os.ReadFile(namespaceInstallYamlPath)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(bytes), "\n") {

			if applicationControllerSettingsParam.resourceRequirements != nil {
				rReq := applicationControllerSettingsParam.resourceRequirements
				line = strings.ReplaceAll(line, "{{ argocd-application-controller-cpu-limit }}", rReq.Limits.Cpu().String())
				line = strings.ReplaceAll(line, "{{ argocd-application-controller-memory-limit }}", rReq.Limits.Memory().String())
				line = strings.ReplaceAll(line, "{{ argocd-application-controller-cpu-request }}", rReq.Requests.Cpu().String())
				line = strings.ReplaceAll(line, "{{ argocd-application-controller-memory-request }}", rReq.Requests.Memory().String())
			}

			if strings.HasPrefix(line, "  controller.status.processors") {

				newFileContents += fmt.Sprintf("  controller.status.processors: \"%d\"\n", applicationControllerSettingsParam.statusProcessors)

			} else if strings.HasPrefix(line, "  controller.operation.processors") {

				newFileContents += fmt.Sprintf("  controller.operation.processors: \"%d\"\n", applicationControllerSettingsParam.operationProcessors)

			} else if strings.HasPrefix(line, "  controller.kubectl.parallelism.limit") {

				newFileContents += fmt.Sprintf("  controller.kubectl.parallelism.limit: \"%d\"\n", applicationControllerSettingsParam.kubectlParallelismLimit)
			} else {
				newFileContents += line + "\n"
			}

		}

		file, err := os.CreateTemp(os.TempDir(), "namespaced-install")
		if err != nil {
			return err
		}

		if err := os.WriteFile(file.Name(), []byte(newFileContents), 0600); err != nil {
			return err
		}

		namespaceInstallYamlPath = file.Name()

	}

	actionOutput("Applying Argo CD manifests")

	stdout, stderr, err = runCommand("kubectl", "apply", "-n", ArgoCDNamespace, "-f", namespaceInstallYamlPath)
	if err != nil {
		fmt.Println(stdout, stderr)
		return err
	}

	actionOutput("Waiting for Argo CD containers to start")

	return waitForPodsToBeReady(ctx, namespace.Name, 6, c)

}

func initialConfiguration(ctx context.Context, applicationControllerSettingsParam *applicationControllerSettings, c *myClient, kLog logr.Logger) error {

	// if err := setupKustomizeArgoCD(ctx, applicationControllerSettingsParam, c); err != nil {
	// 	return err
	// }

	if err := setupOpenShiftGitOps(ctx, applicationControllerSettingsParam, c, kLog); err != nil {
		return err
	}

	if err := createClusterSecret(ctx, ArgoCDNamespace, c.kClient, kLog); err != nil {
		return err
	}

	actionOutput("Create Simple Application to enable Argo CD to watch the cluster")
	app := appv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "enable-cluster-watch",
			Namespace:  ArgoCDNamespace,
			Finalizers: []string{
				// "finalizer.argocd.argoproj.io/background",
				// "resources-finalizer.argocd.argoproj.io",
			},
		},
		Spec: appv1.ApplicationSpec{
			Source: &appv1.ApplicationSource{
				RepoURL: "https://github.com/jgwest/repo-template",
				Path:    "apps/single/",

				// RepoURL:        "https://github.com/managed-gitops-test-data/deployment-permutations-a",
				// Path:           "pathC",
				// TargetRevision: "branchB",

			},
			Destination: appv1.ApplicationDestination{
				Name:      "argo-cd-secret",
				Namespace: ArgoCDNamespace,
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

	if err := c.kClient.Create(ctx, &app); err != nil {
		return err
	}

	actionOutput("Wait for Application to be healthy, to enable cluster watch")
	expectedSyncStatus := appv1.SyncStatusCodeSynced
	expectedHealth := health.HealthStatusHealthy
	if err := waitForArgoCDApplicationSyncStatusAndHealth(ctx, app, &expectedSyncStatus, &expectedHealth, c); err != nil {
		return err
	}

	actionOutput("Wait 60 seconds after enabling cluster watch, to allow application controller to enumerate resources on the cluster")
	time.Sleep(60 * time.Second)

	return nil

}

func createClusterSecret(ctx context.Context, argoCDNamespace string, kClient client.Client, klog logr.Logger) error {
	token, _, err := installServiceAccount(ctx, kClient, "uuid", "kube-system", klog)
	if err != nil {
		return err
	}

	_, apiServerURL, err := extractKubeConfigValues()
	if err != nil {
		return err
	}

	secret, err := generateClusterSecret(ctx, apiServerURL, token, false, "", argoCDNamespace)
	if err != nil {
		return err
	}

	if err := kClient.Get(ctx, client.ObjectKeyFromObject(&secret), &secret); err != nil {

		if apierr.IsNotFound(err) {

			if err := kClient.Create(ctx, &secret); err != nil {
				return err
			}

		} else {
			return err
		}
	}

	return nil
}

func setupOpenShiftGitOps(ctx context.Context, applicationControllerSettingsParam *applicationControllerSettings, c *myClient, klog logr.Logger) error {

	// Create the subscription if it doesn't already exist
	if err := createSubscription(ctx, c); err != nil {
		return err
	}

	actionOutput("Waiting for ArgoCD CRD to exist")
	for {
		cr := apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "argocds.argoproj.io",
			},
		}
		if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(&cr), &cr); err != nil {
			if !apierr.IsNotFound(err) {
				return err
			}
			time.Sleep(1 * time.Second)

		} else {
			break
		}
	}

	if err := recreateArgoCDCR(ctx, applicationControllerSettingsParam, c); err != nil {
		return err
	}

	return nil
}

func createSubscription(ctx context.Context, c *myClient) error {

	actionOutput("Delete remaining ArgoCD CRs")
	if argoCDList, err := c.argoCDClient.Namespace(ArgoCDNamespace).List(ctx, metav1.ListOptions{}); err != nil {
		if !strings.Contains(err.Error(), "the server could not find the requested resource") {
			return err
		}

	} else {
		for _, argoCD := range argoCDList.Items {
			// set deletion timestamp
			if err := c.argoCDClient.Namespace(ArgoCDNamespace).Delete(ctx, argoCD.GetName(), metav1.DeleteOptions{}); err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			}
			// remove the finalizer
			argoCDEntry, err := c.argoCDClient.Namespace(ArgoCDNamespace).Get(ctx, argoCD.GetName(), metav1.GetOptions{})
			if err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			}
			argoCDEntry.SetFinalizers([]string{})
			if _, err := c.argoCDClient.Update(ctx, &argoCD, metav1.UpdateOptions{}); err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			}
		}
	}

	actionOutput("Deleting OpenShift GitOps subscription")
	if err := c.subscriptionClient.Namespace("openshift-operators").Delete(ctx, "openshift-gitops-operator", metav1.DeleteOptions{}); err != nil {
		if !apierr.IsNotFound(err) {
			return err
		}

	}

	actionOutput("Deleting OpenShift GitOps CSVs")
	csvList, err := c.csvClient.Namespace("openshift-operators").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, csv := range csvList.Items {
		if strings.Contains(csv.GetName(), "gitops-operator") {
			if err := c.csvClient.Namespace("openshift-operators").Delete(ctx, csv.GetName(), metav1.DeleteOptions{}); err != nil {
				return err
			}
		}
	}

	actionOutput("Waiting for pods to no longer exist in openshift-operators namespace")
	for {

		var podList corev1.PodList
		if err := c.kClient.List(ctx, &podList, &client.ListOptions{Namespace: "openshift-operators"}); err != nil {
			return err
		}

		if len(podList.Items) != 0 {
			time.Sleep(1 * time.Second)
		} else {
			break
		}
	}

	actionOutput("Deleting existing Argo CD Namespace")

	// Delete existing Argo CD namespace
	if err := deleteResource(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ArgoCDNamespace}}, c); err != nil {
		return err
	}

	actionOutput("Creating new OpenShift GitOps Subscription")

	subscriptionStr := `
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-gitops-operator
  namespace: openshift-operators
spec:
  channel: gitops-1.12
  installPlanApproval: Automatic
  name: openshift-gitops-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  config:
    env:
      - name: DISABLE_DEFAULT_ARGOCD_INSTANCE
        value: "true"
`
	res := map[string]any{}
	if err := yaml.Unmarshal([]byte(subscriptionStr), &res); err != nil {
		return err
	}
	_, err = c.subscriptionClient.Namespace("openshift-operators").Create(ctx, &unstructured.Unstructured{
		Object: res,
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	actionOutput("Waiting for OpenShift GitOps Operator containers to start")

	return waitForPodsToBeReady(ctx, "openshift-operators", 2, c)

}

func getOrCreateServiceAccount(ctx context.Context, k8sClient client.Client, serviceAccountName string, serviceAccountNS string,
	log logr.Logger) (*corev1.ServiceAccount, error) {

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: serviceAccountNS,
		},
	}

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(serviceAccount), serviceAccount); err != nil {
		if !apierr.IsNotFound(err) {
			return nil, fmt.Errorf("unable to retrieve service account '%s': %w", serviceAccount.Name, err)
		}
	} else {
		// Found it, so just return it
		return serviceAccount, nil
	}

	log = log.WithValues("serviceAccount", serviceAccountName, "serviceAccountNS", serviceAccountNS)

	if err := k8sClient.Create(ctx, serviceAccount); err != nil {
		log.Error(err, "Unable to create ServiceAccount")
		return nil, fmt.Errorf("unable to create service account '%s': %w", serviceAccount.Name, err)
	}

	log.Info(fmt.Sprintf("ServiceAccount %s created in namespace %s", serviceAccountName, serviceAccountNS))

	return serviceAccount, nil
}

// generateServiceAccountName encapsulates the logic of what name to use when creating a ServiceAccount for Argo CD to use.
func generateServiceAccountName(uuid string) string {
	return ArgoCDManagerServiceAccountPrefix + uuid
}

func installServiceAccount(ctx context.Context, k8sClient client.Client, uuid string, serviceAccountNS string, log logr.Logger) (string, *corev1.ServiceAccount, error) {

	serviceAccountName := generateServiceAccountName(uuid)

	sa, err := getOrCreateServiceAccount(ctx, k8sClient, serviceAccountName, serviceAccountNS, log)
	if err != nil {
		return "", nil, fmt.Errorf("unable to create or update service account: %v, error: %w", serviceAccountName, err)
	}

	if err := createOrUpdateClusterRoleAndRoleBinding(ctx, uuid, k8sClient, serviceAccountName, serviceAccountNS, log); err != nil {
		return "", nil, fmt.Errorf("unable to create or update role and cluster role binding: %w", err)
	}

	token, err := getOrCreateServiceAccountBearerToken(ctx, k8sClient, serviceAccountName, serviceAccountNS, log)
	if err != nil {
		return "", nil, err
	}

	return token, sa, nil
}

// getOrCreateServiceAccountBearerToken returns a token if there is an existing token secret for a service account.
// If the token secret is missing, it creates a new secret and attach it to the service account
func getOrCreateServiceAccountBearerToken(ctx context.Context, k8sClient client.Client, serviceAccountName string,
	serviceAccountNS string, log logr.Logger) (string, error) {

	tokenSecret, err := createServiceAccountTokenSecret(ctx, k8sClient, serviceAccountName, serviceAccountNS, log)
	if err != nil {
		return "", fmt.Errorf("failed to create a token secret for service account %s: %w", serviceAccountName, err)
	}

	if err := wait.PollImmediate(time.Second*1, time.Second*120, func() (bool, error) {

		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tokenSecret), tokenSecret); err != nil {
			log.Error(err, "unable to retrieve token secret for service account", "serviceAccountName", serviceAccountName)
		}

		// Exit the loop if the token has been set by k8s, continue otherwise.
		_, exists := tokenSecret.Data["token"]
		return exists, nil

	}); err != nil {
		return "", fmt.Errorf("unable to create service account token secret: %w", err)
	}

	tokenSecretValue := tokenSecret.Data["token"]
	return string(tokenSecretValue), nil

}

func getServiceAccountTokenSecret(ctx context.Context, k8sClient client.Client, serviceAccount *corev1.ServiceAccount) (*corev1.Secret, error) {
	secrets := &corev1.SecretList{}
	ns := serviceAccount.Namespace
	opts := []client.ListOption{
		client.InNamespace(ns),
	}

	err := k8sClient.List(ctx, secrets, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve secrets in namespace: %s: %v", ns, err)
	}

	for _, oRef := range secrets.Items {
		var getErr error
		innerSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oRef.Name,
				Namespace: serviceAccount.Namespace,
			},
		}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(innerSecret), innerSecret); err != nil {
			return nil, fmt.Errorf("failed to retrieve secret %q: %v", oRef.Name, getErr)
		}

		if innerSecret.Type == corev1.SecretTypeServiceAccountToken && innerSecret.Annotations["kubernetes.io/service-account.uid"] == string(serviceAccount.UID) {
			return innerSecret, nil
		}
	}

	return nil, nil
}

func createServiceAccountTokenSecret(ctx context.Context, k8sClient client.Client, serviceAccountName, serviceAccountNS string,
	log logr.Logger) (*corev1.Secret, error) {

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: serviceAccountName,
			Namespace:    serviceAccountNS,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: serviceAccountName,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}

	log = log.WithValues("tokenSecretName", tokenSecret.Name, "tokenSecretNamespace", tokenSecret.Namespace)

	if err := k8sClient.Create(ctx, tokenSecret); err != nil {
		log.Error(err, "Unable to create ServiceAccountToken Secret")
		return nil, err
	}
	log.Info("created ServiceAccountToken Secret")

	return tokenSecret, nil
}

func createOrUpdateClusterRoleAndRoleBinding(ctx context.Context, uuid string, k8sClient client.Client,
	serviceAccountName string, serviceAccountNamespace string, log logr.Logger) error {

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: ArgoCDManagerClusterRoleNamePrefix + uuid,
		},
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterRole), clusterRole); err != nil {

		if !apierr.IsNotFound(err) {
			return fmt.Errorf("unable to get cluster role: %w", err)
		}

		log := log.WithValues("clusterRoleName", clusterRole.Name)

		clusterRole.Rules = ArgoCDManagerNamespacePolicyRules
		if err := k8sClient.Create(ctx, clusterRole); err != nil {
			log.Error(err, "Unable to create ClusterRole")
			return fmt.Errorf("unable to create clusterrole: %w", err)
		}
		log.Info("Created ClusterRole")

	} else {
		log := log.WithValues("clusterRoleName", clusterRole.Name)

		clusterRole.Rules = ArgoCDManagerNamespacePolicyRules
		if err := k8sClient.Update(ctx, clusterRole); err != nil {
			log.Error(err, "Unable to update ClusterRole")
			return fmt.Errorf("unable to update cluster role: %w", err)
		}

		log.Info("Created ClusterRole")
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ArgoCDManagerClusterRoleBindingNamePrefix + uuid,
		},
	}
	update := true
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterRoleBinding), clusterRoleBinding); err != nil {
		if !apierr.IsNotFound(err) {
			return fmt.Errorf("unable to get cluster role binding: %w", err)
		}
		update = false
	}

	clusterRoleBinding.RoleRef = rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "ClusterRole",
		Name:     clusterRole.Name,
	}

	clusterRoleBinding.Subjects = []rbacv1.Subject{{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      serviceAccountName,
		Namespace: serviceAccountNamespace,
	}}

	log = log.WithValues("clusterRoleNameBindingName", clusterRoleBinding.Name)

	if update {
		if err := k8sClient.Update(ctx, clusterRoleBinding); err != nil {
			log.Error(err, "Unable to update ClusterRoleBinding")
			return fmt.Errorf("unable to create clusterrole: %w", err)
		}
		log.Info("Updated ClusterRoleBinding")
	} else {
		if err := k8sClient.Create(ctx, clusterRoleBinding); err != nil {
			log.Error(err, "Unable to create ClusterRoleBinding")
			return fmt.Errorf("unable to create clusterrole: %w", err)
		}
		log.Info("Updated ClusterRoleBinding")
	}

	return nil
}

func generateClusterSecret(ctx context.Context, clusterCredentialsHost string, bearerToken string, clusterResources bool, clusterNamespaces string, argoCDNamespace string) (corev1.Secret, error) {

	name := "argo-cd-secret"
	insecureVerifyTLS := true

	clusterSecretConfigJSON := ClusterSecretConfigJSON{
		BearerToken: bearerToken,
		TLSClientConfig: ClusterSecretTLSClientConfigJSON{
			Insecure: insecureVerifyTLS,
		},
	}

	jsonString, err := json.Marshal(clusterSecretConfigJSON)
	if err != nil {
		return corev1.Secret{}, fmt.Errorf("SEVERE: unable to marshal JSON")
	}

	argoCDClusterSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: argoCDNamespace,
			Labels: map[string]string{
				ArgoCDSecretTypeIdentifierKey: ArgoCDSecretClusterTypeValue,
			},
		},
		Data: map[string][]byte{
			"name":   ([]byte)(name),
			"server": ([]byte)(clusterCredentialsHost),
			"config": ([]byte)(string(jsonString)),
		},
	}

	if clusterResources {
		argoCDClusterSecret.Data["clusterResources"] = ([]byte)("true")
	}

	if clusterNamespaces != "" {
		argoCDClusterSecret.Data["namespaces"] = ([]byte)(clusterNamespaces)
	}

	return argoCDClusterSecret, nil

}

const (
	//Secret label key to define secret type.
	ArgoCDSecretTypeIdentifierKey = "argocd.argoproj.io/secret-type" // #nosec G101
	// Secret type for Cluster Secret
	ArgoCDSecretClusterTypeValue = "cluster" // #nosec G101
)

type ClusterSecretTLSClientConfigJSON struct {
	Insecure bool `json:"insecure"`
}
type ClusterSecretConfigJSON struct {
	BearerToken     string                           `json:"bearerToken"`
	TLSClientConfig ClusterSecretTLSClientConfigJSON `json:"tlsClientConfig"`
}

// extractKubeConfigValues returns contents of k8s config from $KUBE_CONFIG, plus server api url (and error)
func extractKubeConfigValues() (string, string, error) {

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()

	config, err := loadingRules.Load()
	if err != nil {
		return "", "", err
	}

	context, ok := config.Contexts[config.CurrentContext]
	if !ok || context == nil {
		return "", "", fmt.Errorf("no context")
	}

	cluster, ok := config.Clusters[context.Cluster]
	if !ok || cluster == nil {
		return "", "", fmt.Errorf("no cluster")
	}

	var kubeConfigDefault string

	paths := loadingRules.Precedence
	{

		for _, path := range paths {

			// fmt.Println("Attempting to read kube config from", path)

			// homeDir, err := os.UserHomeDir()
			// if err != nil {
			// 	return "", "", err
			// }

			_, err = os.Stat(path)
			if err != nil {
				fmt.Println("Unable to resolve path", path, err)
			} else {
				// Success
				kubeConfigDefault = path
				break
			}

		}

		if kubeConfigDefault == "" {
			return "", "", fmt.Errorf("unable to retrieve kube config path")
		}
	}

	kubeConfigContents, err := os.ReadFile(kubeConfigDefault)
	if err != nil {
		return "", "", err
	}

	return string(kubeConfigContents), cluster.Server, nil
}

func generateRestConfig() (*rest.Config, error) {
	var config *rest.Config
	{
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.DefaultClientConfig = &clientcmd.DefaultClientConfig
		overrides := clientcmd.ConfigOverrides{}
		clientConfig := clientcmd.NewInteractiveDeferredLoadingClientConfig(loadingRules, &overrides, os.Stdin)

		var err error
		config, err = clientConfig.ClientConfig()
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		// errors.CheckError(err)
		// errors.CheckError(argoappv1.SetK8SConfigDefaults(config))
	}
	config.QPS = 1000
	return config, nil
}

func createClient() (client.Client, error) {

	config, err := generateRestConfig()
	if err != nil {
		return nil, err
	}

	// kubeClient := kubernetes.NewForConfigOrDie(config)

	if err := apiextensionsv1.AddToScheme(scheme.Scheme); err != nil {
		return nil, err
	}

	if err := appv1.AddToScheme(scheme.Scheme); err != nil {
		return nil, err
	}

	if err := appsv1.AddToScheme(scheme.Scheme); err != nil {
		return nil, err
	}

	kClient, err := client.New(config, client.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		return nil, err
	}
	return kClient, nil
}

type applicationControllerSettings struct {
	operationProcessors     int
	statusProcessors        int
	kubectlParallelismLimit int
	resourceRequirements    *corev1.ResourceRequirements
}

func recreateArgoCDCR(ctx context.Context, applicationControllerSettingsParam *applicationControllerSettings, c *myClient) error {

	actionOutput("Deleting and recreating Argo CD namespace")
	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ArgoCDNamespace,
		},
	}
	if err := deleteAndCreateResource(ctx, &namespace, c); err != nil {
		return err
	}

	// if err := c.kClient.Delete(ctx, &namespace); err != nil {
	// 	if !apierr.IsNotFound(err) {
	// 		return err
	// 	}
	// }

	// actionOutput("Waiting for 'argocd' Namespace to no longer exist")
	// for {
	// 	namespaceGet := namespace.DeepCopy()
	// 	if err := c.kClient.Get(ctx, client.ObjectKeyFromObject(namespaceGet), namespaceGet); err != nil {
	// 		break
	// 	} else {
	// 		time.Sleep(1 * time.Second)
	// 	}
	// }

	// if err := c.kClient.Create(ctx, &namespace); err != nil {
	// 	return err
	// }

	// {

	// 	role := rbacv1.Role{
	// 		ObjectMeta: metav1.ObjectMeta{
	// 			Name:      "role",
	// 			Namespace: namespace.Name,
	// 		},
	// 		Rules: []rbacv1.PolicyRule{
	// 			{
	// 				APIGroups: []string{"*"},
	// 				Resources: []string{"*"},
	// 				Verbs:     []string{"*"},
	// 			},
	// 		},
	// 	}
	// 	if err := c.kClient.Delete(ctx, &role); err != nil {
	// 		if apierr.IsNotFound(err) {
	// 			return err
	// 		}
	// 	}

	// 	if err := c.kClient.Create(ctx, &role); err != nil {
	// 		return err
	// 	}

	// 	roleBinding := rbacv1.RoleBinding{
	// 		ObjectMeta: metav1.ObjectMeta{
	// 			Name:      "rolebinding",
	// 			Namespace: namespace.Name,
	// 		},
	// 		Subjects: []rbacv1.Subject{
	// 			rbacv1.Subject{
	// 				Kind:      rbacv1.ServiceAccountKind,
	// 				Name:      "openshift-gitops-operator-controller-manager",
	// 				Namespace: "openshift-operators",
	// 			},
	// 		},
	// 		RoleRef: rbacv1.RoleRef{
	// 			APIGroup: "rbac.authorization.k8s.io",
	// 			Kind:     "Role",
	// 			Name:     role.Name,
	// 		},
	// 	}
	// 	if err := c.kClient.Delete(ctx, &roleBinding); err != nil {
	// 		if apierr.IsNotFound(err) {
	// 			return err
	// 		}
	// 	}

	// 	if err := c.kClient.Create(ctx, &roleBinding); err != nil {
	// 		return err
	// 	}

	// }

	actionOutput("Creating ArgoCD CR")

	argoCDStr := `
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: argocd
#  finalizers:
#    - argoproj.io/finalizer
spec:
  server:
    autoscale:
      enabled: false
    grpc:
      ingress:
        enabled: false
    ingress:
      enabled: false
    resources:
      limits:
        cpu: 500m
        memory: 256Mi
      requests:
        cpu: 125m
        memory: 128Mi
    route:
      enabled: true
    service:
      type: ''
  grafana:
    enabled: false
    ingress:
      enabled: false
    resources:
      limits:
        cpu: 500m
        memory: 256Mi
      requests:
        cpu: 250m
        memory: 128Mi
    route:
      enabled: false
  monitoring:
    enabled: false
  notifications:
    enabled: false
  prometheus:
    enabled: false
    ingress:
      enabled: false
    route:
      enabled: false
  initialSSHKnownHosts: {}
  sso:
    dex:
      openShiftOAuth: true
      resources:
        limits:
          cpu: 500m
          memory: 256Mi
        requests:
          cpu: 250m
          memory: 128Mi
    provider: dex
  applicationSet:
    resources:
      limits:
        cpu: '2'
        memory: 1Gi
      requests:
        cpu: 250m
        memory: 512Mi
    webhookServer:
      ingress:
        enabled: false
      route:
        enabled: false

#  rbac:
#    policy: g, system:authenticated, role:admin
#    scopes: '[groups]'
    
  rbac:
    defaultPolicy: ''
    policy: |
      g, system:cluster-admins, role:admin
      g, cluster-admins, role:admin
    scopes: '[groups]'
  repo:
    resources:
      limits:
        cpu: '1'
        memory: 1Gi
      requests:
        cpu: 250m
        memory: 256Mi
  resourceExclusions: |
    - apiGroups:
      - tekton.dev
      clusters:
      - '*'
      kinds:
      - TaskRun
      - PipelineRun
  ha:
    enabled: false
    resources:
      limits:
        cpu: 500m
        memory: 256Mi
      requests:
        cpu: 250m
        memory: 128Mi
  tls:
    ca: {}
  redis:
    resources:
      limits:
        cpu: 500m
        memory: 256Mi
      requests:
        cpu: 250m
        memory: 128Mi
`

	// TODO: add support for controlling limits/requests
	if applicationControllerSettingsParam == nil {
		argoCDStr += `
  controller:
    processors: {}
    resources:
      limits:
        cpu: '2'
        memory: 2Gi
      requests:
        cpu: 250m
        memory: 250Mi
    sharding: {}`
	} else {
		argoCDStr += `
  controller:
    processors:
      operation: ` + fmt.Sprintf("%d", applicationControllerSettingsParam.operationProcessors) + ` 
      status:  ` + fmt.Sprintf("%d", applicationControllerSettingsParam.statusProcessors) + ` 
    parallelismLimit: ` + fmt.Sprintf("%d", applicationControllerSettingsParam.kubectlParallelismLimit) + `
    resources:
      limits:
        cpu: '2'
        memory: 2Gi
      requests:
        cpu: 250m
        memory: 250Mi`
	}

	if err := dynamicCreateInNamespace(ctx, argoCDStr, namespace.Name, c.argoCDClient); err != nil {
		return err
	}

	actionOutput("Waiting for Argo CD containers to start")

	return waitForPodsToBeReady(ctx, namespace.Name, 5, c)

}

func waitForPodsToBeReady(ctx context.Context, namespace string, expectedContainers int, c *myClient) error {
	for {

		var podList corev1.PodList

		if err := c.kClient.List(ctx, &podList, &client.ListOptions{Namespace: namespace}); err != nil {
			return err
		}

		containersInReadyState := 0
		for _, pod := range podList.Items {
			for _, containerStatus := range pod.Status.ContainerStatuses {

				if strings.Contains(containerStatus.Name, "dex") {
					continue // skip dex
				}

				if containerStatus.Ready {
					containersInReadyState++
				}
			}
		}

		if containersInReadyState >= expectedContainers {
			break
		} else {
			debugActionOutput(fmt.Sprintf("%v", containersInReadyState))
			time.Sleep(1 * time.Second)
		}

	}

	return nil
}
