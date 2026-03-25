package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	controllerRequeueReady   = 30 * time.Second
	controllerRequeuePending = 10 * time.Second
	controllerRequeueRetry   = 5 * time.Second
)

// connectBrowserPayload is the JSON body for POST /parser/browsers on the controller API.
type connectBrowserPayload struct {
	BrowserID string `json:"browser_id"`
	WsURL     string `json:"ws_url"`
}

// ControllerReconciler reconciles Controller custom resources.
type ControllerReconciler struct {
	client.Client
	Scheme                 *runtime.Scheme
	HTTPClient             *http.Client
	DefaultControllerImage string // fallback image when Controller CR omits spec.image
}

// SetupWithManager registers the reconciler and watches Browser CRs as a secondary resource.
func (r *ControllerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&browserv1.Controller{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&browserv1.Browser{}, handler.EnqueueRequestsFromMapFunc(r.mapBrowserToControllers)).
		Complete(r)
}

// mapBrowserToControllers enqueues all Controller CRs in the same namespace when a Browser changes.
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

// Reconcile is the main reconciliation loop for Controller CRs.
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

	// Ensure child resources
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

// ────────────────────────────────────────────────────────────
// Deletion
// ────────────────────────────────────────────────────────────

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

// ────────────────────────────────────────────────────────────
// Ensure child resources
// ────────────────────────────────────────────────────────────

func (r *ControllerReconciler) ensureControllerDeployment(ctx context.Context, ctrlCR *browserv1.Controller) error {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlCR.Name,
			Namespace: ctrlCR.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		applyControllerDeploymentSpec(deploy, ctrlCR, r.DefaultControllerImage)
		return controllerutil.SetControllerReference(ctrlCR, deploy, r.Scheme)
	})
	return err
}

func (r *ControllerReconciler) ensureControllerService(ctx context.Context, ctrlCR *browserv1.Controller) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlCR.Name,
			Namespace: ctrlCR.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		applyControllerServiceSpec(svc, ctrlCR)
		return controllerutil.SetControllerReference(ctrlCR, svc, r.Scheme)
	})
	return err
}

// ────────────────────────────────────────────────────────────
// Status reconciliation + browser sync
// ────────────────────────────────────────────────────────────

func (r *ControllerReconciler) reconcileControllerStatus(ctx context.Context, ctrlCR *browserv1.Controller) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Find a running, ready controller pod
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

	serviceURL := fmt.Sprintf("http://%s.%s:%d/parser", ctrlCR.Name, ctrlCR.Namespace, controllerPort)

	// Sync browser registrations
	registered, err := r.syncBrowserRegistrations(ctx, ctrlCR, serviceURL)
	if err != nil {
		logger.Info("browser sync had errors (will retry)", "error", err)
	}

	return r.setControllerStatus(ctx, ctrlCR, browserv1.ControllerPhaseRunning,
		"Controller is ready", registered, controllerRequeueReady)
}

func (r *ControllerReconciler) setControllerStatus(
	ctx context.Context,
	ctrlCR *browserv1.Controller,
	phase browserv1.ControllerPhase,
	message string,
	registered []browserv1.RegisteredBrowser,
	requeue time.Duration,
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

// ────────────────────────────────────────────────────────────
// Browser registration sync
// ────────────────────────────────────────────────────────────

func (r *ControllerReconciler) syncBrowserRegistrations(
	ctx context.Context,
	ctrlCR *browserv1.Controller,
	controllerURL string,
) ([]browserv1.RegisteredBrowser, error) {
	logger := log.FromContext(ctx)

	// List Browser CRs in the same namespace
	var browsers browserv1.BrowserList
	listOpts := []client.ListOption{client.InNamespace(ctrlCR.Namespace)}
	if len(ctrlCR.Spec.BrowserSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(ctrlCR.Spec.BrowserSelector))
	}
	if err := r.List(ctx, &browsers, listOpts...); err != nil {
		return nil, fmt.Errorf("list browsers: %w", err)
	}

	// Build desired registration set
	type desired struct {
		name       string
		profileUID string
		wsURL      string
	}
	var desiredSet []desired
	for _, br := range browsers.Items {
		if br.Status.Phase != browserv1.BrowserPhaseRunning || br.Status.WsURL == "" {
			continue
		}
		profileUID := br.Spec.ProfileUID
		if profileUID == "" {
			profileUID = br.Name
		}
		desiredSet = append(desiredSet, desired{
			name:       br.Name,
			profileUID: profileUID,
			wsURL:      br.Status.WsURL,
		})
	}

	// Build current registered set for diffing
	currentMap := make(map[string]string) // profileUID -> wsUrl
	for _, rb := range ctrlCR.Status.RegisteredBrowsers {
		currentMap[rb.ProfileUID] = rb.WsURL
	}

	desiredMap := make(map[string]struct{})
	var lastErr error

	// Register new or changed browsers
	for _, d := range desiredSet {
		desiredMap[d.profileUID] = struct{}{}
		if currentURL, ok := currentMap[d.profileUID]; ok && currentURL == d.wsURL {
			continue // already registered with same URL
		}
		if err := r.registerBrowserWithController(ctx, controllerURL, d.profileUID, d.wsURL); err != nil {
			logger.Info("failed to register browser", "profileUid", d.profileUID, "error", err)
			lastErr = err
		} else {
			logger.V(1).Info("registered browser", "profileUid", d.profileUID, "wsUrl", d.wsURL)
		}
	}

	// Deregister removed browsers
	for uid := range currentMap {
		if _, ok := desiredMap[uid]; !ok {
			if err := r.deregisterBrowserFromController(ctx, controllerURL, uid); err != nil {
				logger.Info("failed to deregister browser", "profileUid", uid, "error", err)
			} else {
				logger.Info("deregistered browser", "profileUid", uid)
			}
		}
	}

	// Build result
	registered := make([]browserv1.RegisteredBrowser, 0, len(desiredSet))
	for _, d := range desiredSet {
		registered = append(registered, browserv1.RegisteredBrowser{
			Name:       d.name,
			ProfileUID: d.profileUID,
			WsURL:      d.wsURL,
		})
	}
	sort.Slice(registered, func(i, j int) bool {
		return registered[i].ProfileUID < registered[j].ProfileUID
	})

	return registered, lastErr
}

func (r *ControllerReconciler) registerBrowserWithController(ctx context.Context, controllerURL, profileUID, wsURL string) error {
	payload := connectBrowserPayload{BrowserID: profileUID, WsURL: wsURL}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/browsers", controllerURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("controller returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (r *ControllerReconciler) deregisterBrowserFromController(ctx context.Context, controllerURL, profileUID string) error {
	url := fmt.Sprintf("%s/browsers/%s", controllerURL, profileUID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 404 is fine — browser was already removed
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("controller returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
