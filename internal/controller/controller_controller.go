package controller

import (
"context"
"fmt"
"sort"
"time"

appsv1 "k8s.io/api/apps/v1"
corev1 "k8s.io/api/core/v1"
metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
apierrors "k8s.io/apimachinery/pkg/api/errors"
"k8s.io/apimachinery/pkg/runtime"
"k8s.io/apimachinery/pkg/types"
ctrl "sigs.k8s.io/controller-runtime"
"sigs.k8s.io/controller-runtime/pkg/client"
"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
"sigs.k8s.io/controller-runtime/pkg/handler"
"sigs.k8s.io/controller-runtime/pkg/log"

browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

const (
controllerFinalizerName = "livellm.io/controller-cleanup"
controllerRequeueReady   = 60 * time.Second
controllerRequeuePending = 10 * time.Second
controllerRequeueRetry   = 5 * time.Second
)

type ControllerReconciler struct {
	client.Client
	Scheme                 *runtime.Scheme
	HTTPClient             interface{}
	DefaultControllerImage string
	RedisURL               string // Redis URL passed to controller pods for browser discovery
}

func (r *ControllerReconciler) SetupWithManager(mgr ctrl.Manager) error {
return ctrl.NewControllerManagedBy(mgr).
For(&browserv1.Controller{}).
Owns(&appsv1.Deployment{}).
Owns(&corev1.Service{}).
Watches(&browserv1.Browser{}, handler.EnqueueRequestsFromMapFunc(r.mapBrowserToControllers)).
Complete(r)
}

func (r *ControllerReconciler) mapBrowserToControllers(ctx context.Context, obj client.Object) []ctrl.Request {
var controllers browserv1.ControllerList
if err := r.List(ctx, &controllers, client.InNamespace(obj.GetNamespace())); err != nil {
return nil
}
requests := make([]ctrl.Request, 0, len(controllers.Items))
for _, c := range controllers.Items {
requests = append(requests, ctrl.Request{
NamespacedName: types.NamespacedName{Name: c.Name, Namespace: c.Namespace},
})
}
return requests
}

func (r *ControllerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
logger := log.FromContext(ctx)

var ctrlCR browserv1.Controller
if err := r.Get(ctx, req.NamespacedName, &ctrlCR); err != nil {
return ctrl.Result{}, client.IgnoreNotFound(err)
}

if !ctrlCR.DeletionTimestamp.IsZero() {
return r.handleControllerDeletion(ctx, &ctrlCR)
}

if !controllerutil.ContainsFinalizer(&ctrlCR, controllerFinalizerName) {
controllerutil.AddFinalizer(&ctrlCR, controllerFinalizerName)
if err := r.Update(ctx, &ctrlCR); err != nil {
return ctrl.Result{}, err
}
}

if err := r.ensureControllerDeployment(ctx, &ctrlCR); err != nil {
logger.Error(err, "failed to ensure controller Deployment")
return ctrl.Result{RequeueAfter: controllerRequeueRetry}, nil
}
if err := r.ensureControllerService(ctx, &ctrlCR); err != nil {
logger.Error(err, "failed to ensure controller Service")
return ctrl.Result{RequeueAfter: controllerRequeueRetry}, nil
}

return r.reconcileControllerStatus(ctx, &ctrlCR)
}

func (r *ControllerReconciler) handleControllerDeletion(ctx context.Context, ctrlCR *browserv1.Controller) (ctrl.Result, error) {
if !controllerutil.ContainsFinalizer(ctrlCR, controllerFinalizerName) {
return ctrl.Result{}, nil
}
controllerutil.RemoveFinalizer(ctrlCR, controllerFinalizerName)
if err := r.Update(ctx, ctrlCR); err != nil {
return ctrl.Result{}, err
}
return ctrl.Result{}, nil
}

func (r *ControllerReconciler) ensureControllerDeployment(ctx context.Context, ctrlCR *browserv1.Controller) error {
deploy := &appsv1.Deployment{
ObjectMeta: metav1.ObjectMeta{Name: ctrlCR.Name, Namespace: ctrlCR.Namespace},
}
_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		applyControllerDeploymentSpec(deploy, ctrlCR, r.DefaultControllerImage, r.RedisURL)
return controllerutil.SetControllerReference(ctrlCR, deploy, r.Scheme)
})
return err
}

func (r *ControllerReconciler) ensureControllerService(ctx context.Context, ctrlCR *browserv1.Controller) error {
svc := &corev1.Service{
ObjectMeta: metav1.ObjectMeta{Name: ctrlCR.Name, Namespace: ctrlCR.Namespace},
}
_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
applyControllerServiceSpec(svc, ctrlCR)
return controllerutil.SetControllerReference(ctrlCR, svc, r.Scheme)
})
return err
}

func (r *ControllerReconciler) reconcileControllerStatus(ctx context.Context, ctrlCR *browserv1.Controller) (ctrl.Result, error) {
logger := log.FromContext(ctx)

var podList corev1.PodList
if err := r.List(ctx, &podList,
client.InNamespace(ctrlCR.Namespace),
client.MatchingLabels(controllerSelectorLabels(ctrlCR.Name)),
); err != nil {
return ctrl.Result{}, err
}

var readyPod *corev1.Pod
for i := range podList.Items {
pod := &podList.Items[i]
if pod.Status.Phase == corev1.PodRunning && isPodReady(pod) && pod.Status.PodIP != "" {
readyPod = pod
break
}
}

if readyPod == nil {
return r.setControllerStatus(ctx, ctrlCR, browserv1.ControllerPhaseCreating,
"Waiting for controller pod to be ready", nil, controllerRequeuePending)
}

var browsers browserv1.BrowserList
listOpts := []client.ListOption{client.InNamespace(ctrlCR.Namespace)}
if len(ctrlCR.Spec.BrowserSelector) > 0 {
listOpts = append(listOpts, client.MatchingLabels(ctrlCR.Spec.BrowserSelector))
}
if err := r.List(ctx, &browsers, listOpts...); err != nil {
logger.Info("failed to list browsers for status", "error", err)
}

registered := make([]browserv1.RegisteredBrowser, 0)
for _, br := range browsers.Items {
if br.Status.Phase != browserv1.BrowserPhaseRunning || br.Status.WsURL == "" {
continue
}
profileUID := br.Spec.ProfileUID
if profileUID == "" {
profileUID = br.Name
}
registered = append(registered, browserv1.RegisteredBrowser{
Name: br.Name, ProfileUID: profileUID, WsURL: br.Status.WsURL,
})
}
sort.Slice(registered, func(i, j int) bool { return registered[i].ProfileUID < registered[j].ProfileUID })

	// Note: browser registration happens via Redis, not via the operator.
	// The controller auto-discovers browsers from Redis and connects to them.
	// This status list is informational only.
	return r.setControllerStatus(ctx, ctrlCR, browserv1.ControllerPhaseRunning,
		"Controller is ready (browsers discovered via Redis)", registered, controllerRequeueReady)
}

func (r *ControllerReconciler) setControllerStatus(
ctx context.Context, ctrlCR *browserv1.Controller, phase browserv1.ControllerPhase,
message string, registered []browserv1.RegisteredBrowser, requeue time.Duration,
) (ctrl.Result, error) {
serviceURL := ""
if phase == browserv1.ControllerPhaseRunning {
serviceURL = fmt.Sprintf("http://%s.%s:%d/parser", ctrlCR.Name, ctrlCR.Namespace, controllerPort)
}
ctrlCR.Status.Phase = phase
ctrlCR.Status.Message = message
ctrlCR.Status.URL = serviceURL
if registered != nil {
ctrlCR.Status.RegisteredBrowsers = registered
ctrlCR.Status.RegisteredBrowserCount = len(registered)
}
if err := r.Status().Update(ctx, ctrlCR); err != nil {
if apierrors.IsConflict(err) {
return ctrl.Result{Requeue: true}, nil
}
return ctrl.Result{}, err
}
return ctrl.Result{RequeueAfter: requeue}, nil
}
