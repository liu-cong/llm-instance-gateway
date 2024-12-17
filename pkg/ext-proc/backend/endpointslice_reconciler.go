package backend

import (
	"context"
	"fmt"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

var (
	serviceOwnerLabel = "kubernetes.io/service-name"
)

type EndpointSliceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Record         record.EventRecorder
	ServerPoolName string
	ServiceName    string
	Zone           string
	Namespace      string
	Datastore      *K8sDatastore
}

func (c *EndpointSliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.V(2).Info("Reconciling EndpointSlice ", req.NamespacedName)

	endpointSlice := &discoveryv1.EndpointSlice{}
	if err := c.Get(ctx, req.NamespacedName, endpointSlice); err != nil {
		klog.Errorf("unable to get EndpointSlice: %v", err)
		return ctrl.Result{}, err
	}

	c.updateDatastore(endpointSlice)

	return ctrl.Result{}, nil
}

func (c *EndpointSliceReconciler) updateDatastore(slice *discoveryv1.EndpointSlice) {
	podMap := make(map[Pod]bool)
	for _, endpoint := range slice.Endpoints {
		klog.V(4).Infof("Zone: %v \n endpoint: %+v \n", c.Zone, endpoint)
		if c.validPod(endpoint) {
			pod := Pod{Name: *&endpoint.TargetRef.Name, Address: endpoint.Addresses[0] + ":" + fmt.Sprint(c.Datastore.InferencePool.Spec.TargetPort)}
			podMap[pod] = true
			c.Datastore.pods.Store(pod, true)
		}
	}

	removeOldPods := func(k, v any) bool {
		pod, ok := k.(Pod)
		if !ok {
			klog.Errorf("Unable to cast key to Pod: %v", k)
			return false
		}
		if _, ok := podMap[pod]; !ok {
			c.Datastore.pods.Delete(pod)
		}
		return true
	}
	c.Datastore.pods.Range(removeOldPods)
}

func (c *EndpointSliceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	llmServerPoolAvailable := func(object client.Object) bool {
		_, err := c.Datastore.getInferencePool()
		if err != nil {
			klog.Warningf("Skipping reconciling EndpointSlice because LLMServerPool is not available yet: %v", err)
		}
		return err == nil
	}

	ownsEndPointSlice := func(object client.Object) bool {
		// Check if the object is an EndpointSlice
		endpointSlice, ok := object.(*discoveryv1.EndpointSlice)
		if !ok {
			return false
		}

		return endpointSlice.ObjectMeta.Labels[serviceOwnerLabel] == c.ServiceName
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&discoveryv1.EndpointSlice{}, builder.WithPredicates(predicate.NewPredicateFuncs(llmServerPoolAvailable), predicate.NewPredicateFuncs(ownsEndPointSlice))).
		Complete(c)
}

func (c *EndpointSliceReconciler) validPod(endpoint discoveryv1.Endpoint) bool {
	validZone := c.Zone == "" || c.Zone != "" && *endpoint.Zone == c.Zone
	return validZone && *endpoint.Conditions.Ready == true

}
