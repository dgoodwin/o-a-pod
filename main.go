package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	kbatch "k8s.io/api/batch/v1"
	kapi "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	openshiftAnsibleImage          = "openshift/origin-ansible:v3.7"
	openshiftAnsibleServiceAccount = "openshift-ansible"
	inventoryConfigMap             = "ansible-inventory"
	sshPrivateKeySecret            = "ssh-private-key"
)

type ansibleRunner struct {
	KubeClient kubernetes.Interface
	Namespace  string
	Image      string
}

func newAnsibleRunner(kubeClient kubernetes.Interface, namespace string) *ansibleRunner {
	return &ansibleRunner{
		KubeClient: kubeClient,
		Namespace:  namespace,
		Image:      openshiftAnsibleImage,
	}
}
func (r *ansibleRunner) createInventoryConfigMap(inventory string) error {
	cfgmap := &kapi.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: inventoryConfigMap,
		},
		Data: map[string]string{
			"hosts": inventory,
		},
	}
	_, err := r.KubeClient.CoreV1().ConfigMaps(r.Namespace).Create(cfgmap)
	if err != nil && kapierrors.IsAlreadyExists(err) {
		// Update existing configmap if it already exists:
		fmt.Println("ansible-hosts configmap already exists, attempting update...")
		_, err = r.KubeClient.CoreV1().ConfigMaps(r.Namespace).Update(cfgmap)
		if err != nil {
			fmt.Printf("error updating ansible-hosts configmap: %s\n", err.Error())
			return err
		}
	} else if err != nil {
		fmt.Printf("error creating ansible-hosts configmap: %s\n", err.Error())
	} else {
		fmt.Printf("ansible-hosts configmap created successfully\n")
	}

	return err
}

func (r *ansibleRunner) RunPlaybook(inventory string, playbook string) error {

	err := r.createInventoryConfigMap(inventory)
	if err != nil {
		return err
	}

	jobName := "openshift-ansible-test-job"
	env := []kapi.EnvVar{
		{
			Name:  "INVENTORY_FILE",
			Value: "/ansible/inventory/hosts",
		},
		{
			Name:  "PLAYBOOK_FILE",
			Value: playbook,
		},
		{
			Name:  "ANSIBLE_HOST_KEY_CHECKING",
			Value: "False",
		},
		{
			Name:  "OPTS",
			Value: "-vvv --private-key=/ansible/ssh/privatekey.pem",
		},
	}
	runAsUser := int64(0)
	sshKeyFileMode := int32(0600)
	podSpec := kapi.PodSpec{
		DNSPolicy:          kapi.DNSClusterFirst,
		RestartPolicy:      kapi.RestartPolicyNever,
		ServiceAccountName: openshiftAnsibleServiceAccount,
		HostNetwork:        true,

		Containers: []kapi.Container{
			{
				Name:  jobName,
				Image: r.Image,
				Env:   env,
				SecurityContext: &kapi.SecurityContext{
					RunAsUser: &runAsUser,
				},
				VolumeMounts: []kapi.VolumeMount{
					{
						Name:      "inventory",
						MountPath: "/ansible/inventory/",
					},
					{
						Name:      "sshkey",
						MountPath: "/ansible/ssh/",
					},
				},
				//Command: []string{"sleep", "1000000"},

				// TODO: drop this once https://github.com/openshift/openshift-ansible/pull/6320 merges, the default run script should then work:
				Command: []string{"ansible-playbook", "-i", "/ansible/inventory/hosts", "--private-key", "/ansible/ssh/privatekey.pem", "/usr/share/ansible/openshift-ansible/playbooks/byo/config.yml"},
			},
		},
		Volumes: []kapi.Volume{
			{
				Name: "inventory",
				VolumeSource: kapi.VolumeSource{
					ConfigMap: &kapi.ConfigMapVolumeSource{
						LocalObjectReference: kapi.LocalObjectReference{
							Name: inventoryConfigMap,
						},
					},
				},
			},
			{
				Name: "sshkey",
				VolumeSource: kapi.VolumeSource{
					Secret: &kapi.SecretVolumeSource{
						SecretName: sshPrivateKeySecret,
						Items: []kapi.KeyToPath{
							{
								Key:  "ssh-privatekey",
								Path: "privatekey.pem",
								Mode: &sshKeyFileMode,
							},
						},
					},
				},
			},
		},
	}

	completions := int32(1)
	deadline := int64(60 * 60) // one hour for now

	meta := metav1.ObjectMeta{
		Name:      jobName,
		Namespace: r.Namespace,
	}

	job := &kbatch.Job{
		ObjectMeta: meta,
		Spec: kbatch.JobSpec{
			Completions:           &completions,
			ActiveDeadlineSeconds: &deadline,
			Template: kapi.PodTemplateSpec{
				Spec: podSpec,
			},
		},
	}

	// Create the job client
	jobClient := r.KubeClient.Batch().Jobs(r.Namespace)

	// Submit the job
	_, err = jobClient.Create(job)
	if err != nil && kapierrors.IsAlreadyExists(err) {
		fmt.Println("job already exists, attempting update...")
		_, err = jobClient.Update(job)
	}
	return err
}

func main() {
	var kubeconfig *string
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	if len(os.Args) != 2 {
		panic("USAGE: ./o-a-pod /path/to/ansible/inventory")
	}

	inventoryBytes, err := ioutil.ReadFile(os.Args[1])
	if err != nil {
		panic(err.Error())
	}
	inventory := string(inventoryBytes)

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	ar := newAnsibleRunner(clientset, "ansible-test")
	err = ar.RunPlaybook(inventory, "playbooks/byo/config.yml")
	if err != nil {
		panic(err.Error())
	}

}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
