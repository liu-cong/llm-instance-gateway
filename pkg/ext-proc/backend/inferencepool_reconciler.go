package backend

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"inference.networking.x-k8s.io/llm-instance-gateway/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	reconcilerNamePrefix = "instance-gateway-"
)

// InferencePoolReconciler utilizes the controller runtime to reconcile Instance Gateway resources
// This implementation is just used for reading & maintaining data sync. The Gateway implementation
// will have the proper controller that will create/manage objects on behalf of the server pool.
type InferencePoolReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Record         record.EventRecorder
	ServerPoolName string
	Namespace      string
	Datastore      *K8sDatastore
	Zone           string
}

func (c *InferencePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.NamespacedName.Name != c.ServerPoolName && req.NamespacedName.Namespace != c.Namespace {
		return ctrl.Result{}, nil
	}
	klog.V(1).Info("reconciling InferencePool", req.NamespacedName)

	serverPool := &v1alpha1.InferencePool{}
	if err := c.Get(ctx, req.NamespacedName, serverPool); err != nil {
		klog.Error(err, "unable to get InferencePool")
		return ctrl.Result{}, err
	}

	c.updateDatastore(serverPool)

	return ctrl.Result{}, nil
}

func (c *InferencePoolReconciler) updateDatastore(serverPool *v1alpha1.InferencePool) {
	if c.Datastore.InferencePool == nil || serverPool.ObjectMeta.ResourceVersion != c.Datastore.InferencePool.ObjectMeta.ResourceVersion {
		c.Datastore.setInferencePool(serverPool)
	}
}

func (c *InferencePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.InferencePool{}).
		Complete(c)
}
