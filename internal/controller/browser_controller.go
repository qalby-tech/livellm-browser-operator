package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	// Requeue intervals
	requeueReady   = 30 * time.Second // healthy check interval
	requeuePending = 10 * time.Second // waiting for pod
	requeueRetry   = 5 * time.Second  // transient error
)

// ────────────────────────────────────────────────────────────
// Launcher API types (JSON from browser's launch.py)
// ────────────────────────────────────────────────────────────

type launcherBrowser struct {
	BrowserID   string  `json:"browser_id"`
	CDPPort     int     `json:"cdp_port"`
	WsEndpoint  string  `json:"ws_endpoint"`
	ProfilePath *string `json:"profile_path"`
}

type createBrowserRequest struct {
	ProfileUID string                   `json:"profile_uid,omitempty"`
	Proxy      map[string]interface{}   `json:"proxy,omitempty"`
	Extensions []string                 `json:"extensions,omitempty"`
	Cookies    []map[string]interface{} `json:"cookies,omitempty"`
}

// ────────────────────────────────────────────────────────────
// BrowserReconciler
// ────────────────────────────────────────────────────────────

// BrowserReconciler reconciles Browser custom resources.
type BrowserReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	HTTPClient          *http.Client
	DefaultBrowserImage string // fallback image when Browser CR omits spec.image
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

	// 5. Reconcile browser status (pod readiness, WS URL)
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
		applyDeploymentSpec(deploy, browser, r.DefaultBrowserImage)
		return controllerutil.SetControllerReference(browser, deploy, r.Scheme)
	})
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
	logger := log.FromContext(ctx)

	// Find running pods by label
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(browser.Namespace),
		client.MatchingLabels(selectorLabels(browser.Name)),
	); err != nil {
		return ctrl.Result{}, err
	}

	// Find a ready pod
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

	podIP := readyPod.Status.PodIP

	// Resolve the profile UID to use
	profileUID := browser.Spec.ProfileUID
	if profileUID == "" {
		profileUID = browser.Name
	}

	// Read cookies from ConfigMap/Secret if specified
	var cookies []map[string]interface{}
	if browser.Spec.Cookies != nil {
		var cookieErr error
		cookies, cookieErr = r.readCookies(ctx, browser)
		if cookieErr != nil {
			logger.Error(cookieErr, "failed to read cookies source")
		}
	}

	// Query/create browser on the launcher
	target, err := r.discoverBrowser(ctx, podIP, profileUID, browser.Spec.Proxy, browser.Spec.Extensions, cookies)
	if err != nil {
		logger.Info("launcher not ready yet", "error", err)
		return r.setStatus(ctx, browser, browserv1.BrowserPhaseCreating, fmt.Sprintf("Launcher: %v", err), requeuePending)
	}

	wsURL := fmt.Sprintf("ws://%s:%d%s", podIP, target.CDPPort, target.WsEndpoint)

	// Update status if anything changed
	if browser.Status.Phase != browserv1.BrowserPhaseRunning || browser.Status.WsURL != wsURL {
		browser.Status.Phase = browserv1.BrowserPhaseRunning
		browser.Status.PodName = readyPod.Name
		browser.Status.PodIP = podIP
		browser.Status.CdpPort = target.CDPPort
		browser.Status.WsEndpoint = target.WsEndpoint
		browser.Status.WsURL = wsURL
		browser.Status.Message = "Browser is ready"

		if err := r.Status().Update(ctx, browser); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("browser is running", "wsUrl", wsURL)
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
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// ────────────────────────────────────────────────────────────
// Launcher API interaction
// ────────────────────────────────────────────────────────────

// discoverBrowser queries the launcher for the target profileUID.
// If it doesn't exist and profileUID != "default", it creates it.
func (r *BrowserReconciler) discoverBrowser(
	ctx context.Context,
	podIP string,
	profileUID string,
	proxy *browserv1.ProxySpec,
	extensions []string,
	cookies []map[string]interface{},
) (*launcherBrowser, error) {

	baseURL := fmt.Sprintf("http://%s:%d", podIP, launcherPort)

	// GET /browsers
	browsers, err := r.listLauncherBrowsers(ctx, baseURL)
	if err != nil {
		return nil, fmt.Errorf("list browsers: %w", err)
	}

	// Look for exact match
	for i := range browsers {
		if browsers[i].BrowserID == profileUID {
			return &browsers[i], nil
		}
	}

	// If the target is "default", it should already exist.
	// If it doesn't, the launcher hasn't finished starting yet.
	if profileUID == "default" {
		if len(browsers) > 0 {
			return &browsers[0], nil
		}
		return nil, fmt.Errorf("default browser not yet available")
	}

	// Create a new browser on the launcher with the desired profile UID
	return r.createLauncherBrowser(ctx, baseURL, profileUID, proxy, extensions, cookies)
}

func (r *BrowserReconciler) listLauncherBrowsers(ctx context.Context, baseURL string) ([]launcherBrowser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/browsers", nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("launcher returned %d: %s", resp.StatusCode, string(body))
	}

	var browsers []launcherBrowser
	if err := json.NewDecoder(resp.Body).Decode(&browsers); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return browsers, nil
}

func (r *BrowserReconciler) createLauncherBrowser(
	ctx context.Context,
	baseURL string,
	profileUID string,
	proxy *browserv1.ProxySpec,
	extensions []string,
	cookies []map[string]interface{},
) (*launcherBrowser, error) {

	payload := createBrowserRequest{ProfileUID: profileUID}
	if proxy != nil && proxy.Server != "" {
		p := map[string]interface{}{"server": proxy.Server}
		if proxy.Username != "" {
			p["username"] = proxy.Username
		}
		if proxy.Password != "" {
			p["password"] = proxy.Password
		}
		if proxy.Bypass != "" {
			p["bypass"] = proxy.Bypass
		}
		payload.Proxy = p
	}
	if len(extensions) > 0 {
		payload.Extensions = extensions
	}
	if len(cookies) > 0 {
		payload.Cookies = cookies
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/browsers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create browser returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result launcherBrowser
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode create response: %w", err)
	}
	return &result, nil
}

// readCookies loads cookies JSON from a ConfigMap or Secret referenced by the Browser spec.
func (r *BrowserReconciler) readCookies(ctx context.Context, browser *browserv1.Browser) ([]map[string]interface{}, error) {
	ref := browser.Spec.Cookies
	if ref == nil {
		return nil, nil
	}

	var raw string

	switch {
	case ref.ConfigMapRef != nil:
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{
			Name: ref.ConfigMapRef.Name, Namespace: browser.Namespace,
		}, &cm); err != nil {
			return nil, fmt.Errorf("get configmap %s: %w", ref.ConfigMapRef.Name, err)
		}
		key := ref.ConfigMapRef.Key
		if key == "" {
			key = "cookies.json"
		}
		raw = cm.Data[key]

	case ref.SecretRef != nil:
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Name: ref.SecretRef.Name, Namespace: browser.Namespace,
		}, &secret); err != nil {
			return nil, fmt.Errorf("get secret %s: %w", ref.SecretRef.Name, err)
		}
		key := ref.SecretRef.Key
		if key == "" {
			key = "cookies.json"
		}
		raw = string(secret.Data[key])
	}

	if raw == "" {
		return nil, nil
	}

	var cookies []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &cookies); err != nil {
		return nil, fmt.Errorf("parse cookies JSON: %w", err)
	}
	return cookies, nil
}
