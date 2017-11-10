// Copyright 2017 Authors of Cilium
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helpers

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	log "github.com/sirupsen/logrus"

	"github.com/cilium/cilium/api/v1/models"
)

const (
	// Annotationv4CIDRName is the annotation name used to store the IPv4
	// pod CIDR in the node's annotations. From pkg/k8s
	Annotationv4CIDRName = "io.cilium.network.ipv4-pod-cidr"
	// Annotationv6CIDRName is the annotation name used to store the IPv6
	// pod CIDR in the node's annotations. From pkg/k8s
	Annotationv6CIDRName = "io.cilium.network.ipv6-pod-cidr"
	kubectl              = "kubectl"
)

// GetCurrentK8SEnv returns the value of K8S_VERSION from the OS environment
func GetCurrentK8SEnv() string { return os.Getenv("K8S_VERSION") }

// Kubectl kubectl command helper to connect to Kubectl instance
type Kubectl struct {
	Node *SSHMeta //helpers.SSHMeta struct to connect to ssh

	logCxt *log.Entry //log context with test fields
}

// CreateKubectl initializes a Kubectl helper with the provided vmName and log
func CreateKubectl(vmName string, log *log.Entry) *Kubectl {
	node := GetVagrantSSHMetadata(vmName)
	if node == nil {
		return nil
	}

	return &Kubectl{
		Node:   node,
		logCxt: log,
	}
}

// Exec runs the provided command in a pod running in a specific namespace
func (kub *Kubectl) Exec(namespace string, pod string, cmd string) (string, error) {
	command := fmt.Sprintf("%s exec -n %s %s -- %s", kubectl, namespace, pod, cmd)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	exit := kub.Node.Execute(command, stdout, stderr)
	if exit == false {
		// TODO: Return CmdRes here
		// Return the string is not fired on the assertion :\ Need to check
		kub.logCxt.Errorf(
			"Exec command failed '%s' pod='%s' error='%s||%s'",
			cmd, pod, stdout.String(), stderr.String())
		return "", fmt.Errorf("Exec: command '%s' failed '%s'", command, stdout.String())
	}
	return stdout.String(), nil
}

// Get retrieves the provided Kubernetes objects from the specified namespace
func (kub *Kubectl) Get(namespace string, command string) *CmdRes {
	return kub.Node.Exec(fmt.Sprintf(
		"%s -n %s get %s -o json", kubectl, namespace, command))
}

// GetPods gets all of the pods in the given namespace that match the provided
// filter.
func (kub *Kubectl) GetPods(namespace string, filter string) *CmdRes {
	return kub.Node.Exec(fmt.Sprintf("%s -n %s get pods %s -o json", kubectl, namespace, filter))
}

//GetPodNames returns the names of all of the pods that are labelled with label
//in the specified namespace, along with an error if the pod names cannot be
//retrieved.
func (kub *Kubectl) GetPodNames(namespace string, label string) ([]string, error) {
	stdout := new(bytes.Buffer)
	filter := "-o jsonpath='{.items[*].metadata.name}'"
	exit := kub.Node.Execute(
		fmt.Sprintf("%s -n %s get pods -l %s %s", kubectl, namespace, label, filter),
		stdout, nil)

	if exit == false {
		return nil, fmt.Errorf(
			"could not find pods in namespace %q with label %q", namespace, label)
	}

	out := strings.Trim(stdout.String(), "\n")
	if len(out) == 0 {
		//Small hack. String split always return an array with an empty string
		return []string{}, nil
	}
	return strings.Split(out, " "), nil
}

//Logs returns CmdRes with the output of kubectl logs <pod> command
func (kub *Kubectl) Logs(namespace string, pod string) *CmdRes {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	exit := kub.Node.Execute(
		fmt.Sprintf("%s -n %s logs %s", kubectl, namespace, pod),
		stdout, stderr)
	return &CmdRes{
		cmd:    "",
		stdout: stdout,
		stderr: stderr,
		exit:   exit,
	}
}

//ManifestsPath returns the manifests path for the k8s version
func (kub *Kubectl) ManifestsPath() string {
	return fmt.Sprintf("%s/k8sT/manifests/%s", basePath, GetCurrentK8SEnv())
}

//NodeCleanMetadata delete all cilium metadata info to all nodes
func (kub *Kubectl) NodeCleanMetadata() error {
	metadata := []string{
		Annotationv4CIDRName,
		Annotationv6CIDRName,
	}

	data := kub.Node.Exec(fmt.Sprintf("%s get nodes -o jsonpath='{.items[*].metadata.name}'", kubectl))
	if !data.WasSuccessful() {
		return fmt.Errorf("could not get nodes via %s: %s", kubectl, data.CombineOutput())
	}
	for _, node := range strings.Split(data.Output().String(), " ") {
		for _, label := range metadata {
			kub.Node.Exec(fmt.Sprintf("%s annotate nodes %s %s", kubectl, node, label))
		}
	}
	return nil
}

//WaitforPods waits until pods are ready in the specified namespace using the provided JSONPath filter. It waits up to timeout seconds for the command to complete. Returns true if the command succeeded within the specified timeout, and an error if the command failed or did not succeed during the specified timeout.
func (kub *Kubectl) WaitforPods(namespace string, filter string, timeout time.Duration) (bool, error) {
	body := func() bool {
		var jsonPath = "{.items[*].status.containerStatuses[*].ready}"
		data, err := kub.GetPods(namespace, filter).Filter(jsonPath)
		if err != nil {
			kub.logCxt.Errorf("could not get pods: %s", err)
			return false
		}

		valid := true
		result := strings.Split(data.String(), " ")
		for _, v := range result {
			if val, _ := govalidator.ToBoolean(v); val == false {
				valid = false
				break
			}
		}
		if valid == true {
			return true
		}
		kub.logCxt.WithFields(log.Fields{
			"namespace": namespace,
			"filter":    filter,
			"data":      data,
		}).Info("WaitforPods: pods are not ready")
		return false
	}
	err := WithTimeout(body, "could not get Pods", &TimeoutConfig{Timeout: timeout})
	if err != nil {
		return false, err
	}
	return true, nil
}

//Apply applies the Kubernetes manifest located at path filepath
func (kub *Kubectl) Apply(filePath string) *CmdRes {
	kub.logCxt.Debugf("applying %s", filePath)
	return kub.Node.Exec(
		fmt.Sprintf("%s apply -f  %s", kubectl, filePath))
}

//Delete Deletes the Kubernetes manifest at path filepath.
func (kub *Kubectl) Delete(filePath string) *CmdRes {
	kub.logCxt.Debugf("deleting %s", filePath)
	return kub.Node.Exec(
		fmt.Sprintf("%s delete -f  %s", kubectl, filePath))
}

// GetCiliumPods returns a list of all Cilium pods in the specified namespace,
// and an error if the Cilium pods were not able to be retrieved.
func (kub *Kubectl) GetCiliumPods(namespace string) ([]string, error) {
	return kub.GetPodNames(namespace, "k8s-app=cilium")
}

// CiliumEndpointsList returns the result of `cilium endpoint list` from the
// specified pod.
func (kub *Kubectl) CiliumEndpointsList(pod string) *CmdRes {
	return kub.CiliumExec(pod, "cilium endpoint list -o json")
}

// CiliumEndpointsListByLabel returns a list of endpoints
func (kub *Kubectl) CiliumEndpointsListByLabel(pod, label string) (EndPointMap, error) {
	result := make(EndPointMap)
	var data []models.Endpoint
	eps := kub.CiliumEndpointsList(pod)

	err := eps.UnMarshal(&data)
	if err != nil {
		return nil, err
	}

	for _, ep := range data {
		for _, orchLabel := range ep.Labels.OrchestrationIdentity {
			if label == orchLabel {
				result[ep.ContainerName] = ep
				break
			}
		}

	}
	return result, nil
}

//CiliumEndpointWait waits until all endpoints managed by the specified Cilium
//pod are ready. Returns false if the command to retrieve the state of the
//endpoints times out.
func (kub *Kubectl) CiliumEndpointWait(pod string) bool {

	body := func() bool {
		status, err := kub.CiliumEndpointsList(pod).Filter("{[*].state}")
		if err != nil {
			return false
		}

		var valid, invalid int
		for _, endpoint := range strings.Split(status.String(), " ") {
			if endpoint != "ready" {
				invalid++
			} else {
				valid++
			}
		}
		if invalid == 0 {
			return true
		}
		kub.logCxt.WithFields(log.Fields{
			"pod":     pod,
			"valid":   valid,
			"invalid": invalid,
		}).Info("Waiting for cilium endpoints")
		return false
	}

	err := WithTimeout(body, "cannot retrieve endpoints", &TimeoutConfig{Timeout: timeout})
	if err != nil {
		return false
	}
	return true
}

//CiliumEndpointPolicyVersion returns a map with the endpoint id and the policy
//revision that are in a cilium pod
func (kub *Kubectl) CiliumEndpointPolicyVersion(pod string) map[string]int64 {
	result := map[string]int64{}
	filter := `{range [*]}{@.id}{"="}{@.policy-revision}{"\n"}{end}`

	data := kub.CiliumExec(
		pod,
		fmt.Sprintf("cilium endpoint list -o jsonpath='%s'", filter))
	for k, v := range data.KVOutput() {
		val, _ := govalidator.ToInt(v)
		result[k] = val
	}
	return result
}

//CiliumExec runs cmd in the specified Cilium pod
func (kub *Kubectl) CiliumExec(pod string, cmd string) *CmdRes {
	cmd = fmt.Sprintf("%s exec -n kube-system %s -- %s", kubectl, pod, cmd)
	return kub.Node.Exec(cmd)
}

//CiliumNodesWait When a new cilium DS is set, the status can be ok but tunnels
//are not in place yet. Checking the `kubectl get nodes` we can know if cilium
//added the node correctly. In case of timeout will return an error
func (kub *Kubectl) CiliumNodesWait() (bool, error) {
	body := func() bool {
		filter := `{range .items[*]}{@.metadata.name}{"="}{@.metadata.annotations.io\.cilium\.network\.ipv4-pod-cidr}{"\n"}{end}`
		data := kub.Node.Exec(fmt.Sprintf(
			"%s get nodes -o jsonpath='%s'", kubectl, filter))
		if !data.WasSuccessful() {
			return false
		}
		result := data.KVOutput()
		for k, v := range result {
			if v == "" {
				kub.logCxt.Infof("Kubernetes node %q does not have Cilium metadata", k)
				return false
			}
			kub.logCxt.Infof("Kubernetes node %q IPv4 address: %q", k, v)
		}
		return true
	}
	err := WithTimeout(body, "Kubernetes node does not have cilium metadata", &TimeoutConfig{Timeout: timeout})
	if err != nil {
		return false, err
	}
	return true, nil
}

//CiliumPolicyRevision returns the policy revision in the specified Cilium pod.
//If the policy revision cannot be retrieved, returns an error.
func (kub *Kubectl) CiliumPolicyRevision(pod string) (int, error) {
	// FIXME GH-1725
	res := kub.CiliumExec(pod, "cilium policy get | grep Revision | awk '{print $2}'")

	if !res.WasSuccessful() {
		return -1, fmt.Errorf("Cannot get the revision %s", res.Output())
	}
	revi, err := res.IntOutput()
	if err != nil {
		kub.logCxt.Errorf("Revision on pod '%s' is not valid '%s'", pod, res.CombineOutput())
		return -1, err
	}
	return revi, nil
}

//CiliumImportPolicy imports the policy stored in path filepath and waits up
//until timeout seconds for the policy to be applied in all Cilium endpoints.
//Returns an error if the command fails or times out.
func (kub *Kubectl) CiliumImportPolicy(namespace string, filepath string, timeout time.Duration) (string, error) {
	var revision int
	revisions := map[string]int{}

	kub.logCxt.Infof("Importing policy '%s'", filepath)
	pods, err := kub.GetCiliumPods(namespace)
	if err != nil {
		return "", err
	}

	for _, v := range pods {
		revi, err := kub.CiliumPolicyRevision(v)
		if err != nil {
			return "", err
		}
		revisions[v] = revi
		kub.logCxt.Infof("CiliumImportPolicy: pod %q has revision %v", v, revi)
	}

	kub.logCxt.Infof("CiliumImportPolicy: path=%q with revision %q", filepath, revision)
	if status := kub.Apply(filepath); !status.WasSuccessful() {
		return "", fmt.Errorf("cannot apply policy %q", filepath)
	}

	body := func() bool {
		waitingRev := map[string]int{}

		valid := true
		for _, v := range pods {
			revi, err := kub.CiliumPolicyRevision(v)
			if err != nil {
				kub.logCxt.Errorf("CiliumImportPolicy: error on get revision %s", err)
				return false
			}
			if revi <= revisions[v] {
				kub.logCxt.Infof("CiliumImportPolicy: Invalid revision(%v) for pod '%s' was on '%v'", revi, v, revisions[v])
				valid = false
			} else {
				waitingRev[v] = revi
			}
		}

		if valid == true {
			//Wait until all the pods are synced
			for pod, rev := range waitingRev {
				kub.logCxt.Infof("CiliumImportPolicy: Wait for endpoints to sync on pod '%s'", pod)
				kub.Exec(namespace, pod, fmt.Sprintf("cilium policy wait %d", rev))
				kub.logCxt.Infof("CiliumImportPolicy: reivision %d in pod '%s' is ready", rev, pod)
			}
			return true
		}
		return false
	}
	err = WithTimeout(
		body,
		"cannot import policy correctly; command timed out",
		&TimeoutConfig{Timeout: timeout})
	if err != nil {
		return "", err
	}
	return "", nil
}

//CiliumReport report the cilium pod to the log and apppend the logs for the
//given commands. Return err in case of any problem
func (kub *Kubectl) CiliumReport(namespace string, pod string, commands []string) error {
	wr := kub.logCxt.Logger.Out
	fmt.Fprint(wr, "StackTrace Begin\n")
	data := kub.Logs(namespace, pod)
	fmt.Fprintln(wr, data.Output())

	data = kub.Node.Exec(fmt.Sprintf("%s get pods -o wide", kubectl))
	fmt.Fprintln(wr, data.Output())

	for _, cmd := range commands {
		command := fmt.Sprintf("%s exec -n %s %s -- %s", kubectl, namespace, pod, cmd)
		out := kub.Node.Exec(command)
		fmt.Fprintln(wr, out.CombineOutput())
	}
	fmt.Fprint(wr, "StackTrace Ends\n")
	return nil
}

//GetCiliumPodOnNode returns the name of the Cilium pod that is running on / in
//the specified node / namespace.
func (kub *Kubectl) GetCiliumPodOnNode(namespace string, node string) (string, error) {
	filter := fmt.Sprintf(
		"-o jsonpath='{.items[?(@.spec.nodeName == \"%s\")].metadata.name}'", node)

	res := kub.Node.Exec(fmt.Sprintf(
		"%s -n %s get pods -l k8s-app=cilium %s", kubectl, namespace, filter))
	if !res.WasSuccessful() {
		return "", fmt.Errorf("Cilium pod not found on node '%s'", node)
	}

	return res.Output().String(), nil
}

//EndPointMap Map with all the endpoints in cilium
type EndPointMap map[string]models.Endpoint

//GetPolicyStatus returns a mapping of how many endpoints have policy
//enforcement enabled and disabled.
func (epMap *EndPointMap) GetPolicyStatus() map[string]int {
	result := map[string]int{
		Enabled:  0,
		Disabled: 0,
	}

	for _, ep := range *epMap {
		if *ep.PolicyEnabled == true {
			result[Enabled]++
		} else {
			result[Disabled]++
		}
	}
	return result
}

//AreReady return true if all cilium endpoints are in 'ready' state
func (epMap *EndPointMap) AreReady() bool {
	for _, ep := range *epMap {
		if ep.State != "ready" {
			return false
		}
	}
	return true
}
