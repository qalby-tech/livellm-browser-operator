package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

const (
	finalizerName = "livellm.io/browser-cleanup"

	requeueReady   = 30 * time.Second
	requeuePending = 10 * time.Second
	requeueRetry   = 5 * time.Second
)

// ────────────────────────────────────────────────────────────
// BrowserReconciler
// ────────────────────────────────────────────────────────────

// BrowserReconciler reconciles Browser custom resources.
type BrowserReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	DefaultBrowserImage      string
	DefaultBrowserPullPolicy string
	DefaultBrowserEnv        []corev1.EnvVar
	DefaultBrowserResources  *browserv1.ResourcesSpec
}

// SetupWithManager registers the reconciler with the manager.
func (r *BrowserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&browserv1.Browser{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// Reconcile is the main reconciliation loop.
func (r *BrowserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the Browser CR
	var browser browserv1.Browser
	if err := r.Get(ctx, req.NamespacedName, &browser); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Handle deletion (finalizer)
	if !browser.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &browser)
	}

	// 3. Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&browser, finalizerName) {
		controllerutil.AddFinalizer(&browser, finalizerName)
		if err := r.Update(ctx, &browser); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Ensure child resources exist
	if err := r.ensurePVC(ctx, &browser); err != nil {
		logger.Error(err, "failed to ensure PVC")
		return ctrl.Result{RequeueAfter: requeueRetry}, nil
	}
	if err := r.ensureDeployment(ctx, &browser); err != nil {
		logger.Error(err, "failed to ensure Deployment")
		return ctrl.Result{RequeueAfter: requeueRetry}, nil
	}
	if err := r.ensureService(ctx, &browser); err != nil {
		logger.Error(err, "failed to ensure Service")
		return ctrl.Result{RequeueAfter: requeueRetry}, nil
	}

	// 5. Reconcile browser status (pod readiness)
	return r.reconcileStatus(ctx, &browser)
}

// ────────────────────────────────────────────────────────────
// Deletion
// ────────────────────────────────────────────────────────────

func (r *BrowserReconciler) handleDeletion(ctx context.Context, browser *browserv1.Browser) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(browser, finalizerName) {
		return ctrl.Result{}, nil
	}

	// Delete PVC if reclaimPolicy == Delete
	if browser.Spec.ReclaimPolicy == "Delete" {
		pvcName := fmt.Sprintf("%s-profile", browser.Name)
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: browser.Namespace,
			},
		}
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to delete PVC")
		} else {
			logger.Info("deleted PVC", "pvc", pvcName)
		}
	} else {
		logger.Info("PVC retained", "pvc", fmt.Sprintf("%s-profile", browser.Name))
	}

	// Remove finalizer → k8s garbage-collects owned Deployment + Service
	controllerutil.RemoveFinalizer(browser, finalizerName)
	if err := r.Update(ctx, browser); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("browser deleted", "name", browser.Name)
	return ctrl.Result{}, nil
}

// ────────────────────────────────────────────────────────────
// Ensure child resources
// ────────────────────────────────────────────────────────────

func (r *BrowserReconciler) ensurePVC(ctx context.Context, browser *browserv1.Browser) error {
	pvcName := fmt.Sprintf("%s-profile", browser.Name)
	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: browser.Namespace}, &existing)
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	// Create new PVC — deliberately NOT owned (survives CR deletion)
	pvc := buildPVC(browser)
	return r.Create(ctx, pvc)
}

func (r *BrowserReconciler) ensureDeployment(ctx context.Context, browser *browserv1.Browser) error {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      browser.Name,
			Namespace: browser.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		applyDeploymentSpec(deploy, browser, r.DefaultBrowserImage, r.DefaultBrowserPullPolicy, r.DefaultBrowserEnv, r.DefaultBrowserResources)
		if err := controllerutil.SetControllerReference(browser, deploy, r.Scheme); err != nil {
			return err
		}
		return nil
	})

	// Conflict is expected in Kubernetes — just return nil so the reconcile
	// loop succeeds and the next periodic requeue picks up the latest state.
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func (r *BrowserReconciler) ensureService(ctx context.Context, browser *browserv1.Browser) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      browser.Name,
			Namespace: browser.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		applyServiceSpec(svc, browser)
		return controllerutil.SetControllerReference(browser, svc, r.Scheme)
	})
	return err
}

// ────────────────────────────────────────────────────────────
// Status reconciliation
// ────────────────────────────────────────────────────────────

func (r *BrowserReconciler) reconcileStatus(ctx context.Context, browser *browserv1.Browser) (ctrl.Result, error) {
	if !browserWorkloadWanted(browser) {
		return r.reconcileStoppedStatus(ctx, browser)
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(browser.Namespace),
		client.MatchingLabels(selectorLabels(browser.Name)),
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
		return r.setStatus(ctx, browser, browserv1.BrowserPhaseCreating, "Waiting for browser pod to be ready", requeuePending)
	}

	profileUID := browser.Spec.ProfileUID
	if profileUID == "" {
		profileUID = browser.Name
	}

	return r.reconcileBrowserState(ctx, browser, readyPod, profileUID)
}

// reconcileStoppedStatus updates status when spec.running is false (Deployment scaled to 0).
func (r *BrowserReconciler) reconcileStoppedStatus(ctx context.Context, browser *browserv1.Browser) (ctrl.Result, error) {
	msg := "Scaled to zero (spec.running=false)"
	if browser.Status.Phase != browserv1.BrowserPhaseStopped ||
		browser.Status.Message != msg ||
		browser.Status.PodName != "" ||
		browser.Status.PodIP != "" ||
		browser.Status.CdpPort != 0 ||
		browser.Status.WsURL != "" {

		browser.Status.Phase = browserv1.BrowserPhaseStopped
		browser.Status.Message = msg
		browser.Status.PodName = ""
		browser.Status.PodIP = ""
		browser.Status.CdpPort = 0
		browser.Status.WsURL = ""
		if err := r.Status().Update(ctx, browser); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeueReady}, nil
}

// setStatus is a helper to update phase/message and requeue.
func (r *BrowserReconciler) setStatus(
	ctx context.Context,
	browser *browserv1.Browser,
	phase browserv1.BrowserPhase,
	message string,
	requeue time.Duration,
) (ctrl.Result, error) {
	if browser.Status.Phase != phase || browser.Status.Message != message {
		browser.Status.Phase = phase
		browser.Status.Message = message
		if err := r.Status().Update(ctx, browser); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// ────────────────────────────────────────────────────────────
// Deterministic state (no discovery service)
// ────────────────────────────────────────────────────────────

func (r *BrowserReconciler) reconcileBrowserState(ctx context.Context, browser *browserv1.Browser, readyPod *corev1.Pod, profileUID string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// One browser per pod with a fixed CDP proxy port fronted by a stable
	// Service — so the CDP ws_url is deterministic and never drifts. The
	// in-pod proxy rewrites the ws path across Chrome restarts and the Service
	// keeps a stable DNS name across pod restarts. No Redis, no discovery.
	wsURL := fmt.Sprintf("ws://%s.%s.svc.cluster.local:%d/devtools/browser/%s",
		browser.Name, browser.Namespace, cdpPort, profileUID)

	if browser.Status.Phase != browserv1.BrowserPhaseRunning ||
		browser.Status.WsURL != wsURL ||
		browser.Status.PodName != readyPod.Name {
		browser.Status.Phase = browserv1.BrowserPhaseRunning
		browser.Status.PodName = readyPod.Name
		browser.Status.PodIP = readyPod.Status.PodIP
		browser.Status.CdpPort = cdpPort
		browser.Status.WsURL = wsURL
		browser.Status.Message = "Browser is ready"

		if err := r.Status().Update(ctx, browser); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		logger.Info("browser is running", "wsUrl", wsURL)
	}

	return ctrl.Result{RequeueAfter: requeueReady}, nil
}
