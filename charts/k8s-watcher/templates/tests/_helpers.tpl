{{/*
Helpers for the `helm test` checks (ev-lane.yaml, cm-lane.yaml). Each check
is a pod that runs a short shell script with kubectl: it creates a fixture
in the test namespace, waits for the Result its lane should write, then
deletes the fixture and that Result.
*/}}

{{/* The namespace the checks create their fixtures in. */}}
{{- define "k8s-watcher.testNamespace" -}}
{{- printf "%s-test" (include "k8s-watcher.fullname" . | trunc 58 | trimSuffix "-") -}}
{{- end -}}

{{/*
Labels for the test resources. A different app.kubernetes.io/name keeps the
test pods out of the watcher's Service and Deployment selectors.
*/}}
{{- define "k8s-watcher.testLabels" -}}
helm.sh/chart: {{ include "k8s-watcher.chart" . }}
app.kubernetes.io/name: {{ include "k8s-watcher.name" . }}-test
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: test
{{- end -}}

{{/*
Hook annotations for the test resources; the argument is the hook weight.
A passing run deletes everything it created. A failing run keeps the failed
check pod, whose log has the test namespace's events, until the next
`helm test` replaces it (before-hook-creation); Helm 3.19 and later delete
the other test resources, the namespace included. `helm uninstall` does not
remove hook resources.
*/}}
{{- define "k8s-watcher.testHook" -}}
helm.sh/hook: test
helm.sh/hook-weight: {{ . | quote }}
helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
{{- end -}}

{{- define "k8s-watcher.testImage" -}}
{{- $ref := printf "%s:%s" .Values.tests.image.repository .Values.tests.image.tag -}}
{{- with .Values.tests.image.digest -}}
{{- $ref = printf "%s@%s" $ref . -}}
{{- end -}}
{{- $ref -}}
{{- end -}}

{{/*
The ServiceAccount a check runs as, and its two namespaced Roles:
  - <name>-fixtures: .rules, on its fixture in the test namespace, plus
    list on events there, for the FAIL output;
  - <name>-results: list and delete on Results in resultNamespace, to find
    the lane's Result and to remove it afterwards.
Nothing cluster-scoped. Called with (dict "root" $ "name" ... "rules" ...).
*/}}
{{- define "k8s-watcher.testAccess" -}}
{{- $root := .root -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "k8s-watcher.testLabels" $root | nindent 4 }}
  annotations:
    {{- include "k8s-watcher.testHook" "-10" | nindent 4 }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .name }}-fixtures
  namespace: {{ include "k8s-watcher.testNamespace" $root }}
  labels:
    {{- include "k8s-watcher.testLabels" $root | nindent 4 }}
  annotations:
    {{- include "k8s-watcher.testHook" "-9" | nindent 4 }}
rules:
  {{- toYaml .rules | nindent 2 }}
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .name }}-fixtures
  namespace: {{ include "k8s-watcher.testNamespace" $root }}
  labels:
    {{- include "k8s-watcher.testLabels" $root | nindent 4 }}
  annotations:
    {{- include "k8s-watcher.testHook" "-8" | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .name }}-fixtures
subjects:
  - kind: ServiceAccount
    name: {{ .name }}
    namespace: {{ $root.Release.Namespace }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .name }}-results
  namespace: {{ $root.Values.resultNamespace }}
  labels:
    {{- include "k8s-watcher.testLabels" $root | nindent 4 }}
  annotations:
    {{- include "k8s-watcher.testHook" "-9" | nindent 4 }}
rules:
  - apiGroups: ["core.k8sgpt.ai"]
    resources: ["results"]
    verbs: ["list", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .name }}-results
  namespace: {{ $root.Values.resultNamespace }}
  labels:
    {{- include "k8s-watcher.testLabels" $root | nindent 4 }}
  annotations:
    {{- include "k8s-watcher.testHook" "-8" | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .name }}-results
subjects:
  - kind: ServiceAccount
    name: {{ .name }}
    namespace: {{ $root.Release.Namespace }}
{{- end -}}

{{/*
The check pod. It runs in the release namespace, so `helm test --logs`
finds it. Called with (dict "root" $ "name" ... "script" ...); the script
runs after watcher.testLib.
*/}}
{{- define "k8s-watcher.testPod" -}}
{{- $root := .root -}}
apiVersion: v1
kind: Pod
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "k8s-watcher.testLabels" $root | nindent 4 }}
  annotations:
    {{- include "k8s-watcher.testHook" "0" | nindent 4 }}
spec:
  restartPolicy: Never
  serviceAccountName: {{ .name }}
  {{- with $root.Values.podSecurityContext }}
  securityContext:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  containers:
    - name: check
      image: {{ include "k8s-watcher.testImage" $root | quote }}
      imagePullPolicy: {{ $root.Values.tests.image.pullPolicy }}
      command: ["/bin/sh", "-c"]
      args:
        - |
          {{- include "k8s-watcher.testLib" $root | nindent 10 }}
          {{- printf "\n" }}
          {{- .script | nindent 10 }}
      env:
        - name: TEST_NAMESPACE
          value: {{ include "k8s-watcher.testNamespace" $root | quote }}
        - name: RESULT_NAMESPACE
          value: {{ $root.Values.resultNamespace | quote }}
        - name: WATCHER_NAMESPACE
          value: {{ $root.Release.Namespace | quote }}
        - name: WATCHER
          value: {{ include "k8s-watcher.fullname" $root | quote }}
        - name: TIMEOUT
          value: {{ $root.Values.tests.timeoutSeconds | quote }}
        # kubectl keeps a discovery cache under $HOME, and the root
        # filesystem may be read-only.
        - name: HOME
          value: /tmp
      {{- with $root.Values.containerSecurityContext }}
      securityContext:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      volumeMounts:
        - name: tmp
          mountPath: /tmp
  volumes:
    - name: tmp
      emptyDir: {}
{{- end -}}

{{/*
Shell functions the check scripts share. Each takes the Result it looks for
as KIND NAME BACKEND SELECTOR: the Result's spec.kind, spec.name and
spec.backend, and a kubectl label selector.
*/}}
{{- define "k8s-watcher.testLib" -}}
set -eu -o pipefail

# results: the names of the matching Results in $RESULT_NAMESPACE.
results() {
  kubectl -n "$RESULT_NAMESPACE" get results.core.k8sgpt.ai -l "$4" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.kind}{"\t"}{.spec.name}{"\t"}{.spec.backend}{"\n"}{end}' |
    awk -F '\t' -v kind="$1" -v name="$2" -v backend="$3" \
      '$2 == kind && $3 == name && $4 == backend { print $1 }'
}

# await: polls every 2s until a matching Result exists, for at most
# $TIMEOUT seconds.
await() {
  deadline=$(( $(date +%s) + TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if ! found=$(results "$@"); then
      echo "FAIL: cannot list Results in $RESULT_NAMESPACE"
      return 1
    fi
    if [ -n "$found" ]; then
      echo "PASS: Result $RESULT_NAMESPACE/$found reports $1 $2"
      return 0
    fi
    sleep 2
  done
  echo "FAIL: no Result for $1 $2 within ${TIMEOUT}s"
  echo "events in $TEST_NAMESPACE:"
  kubectl -n "$TEST_NAMESPACE" get events || true
  echo "watcher logs: kubectl -n $WATCHER_NAMESPACE logs deploy/$WATCHER"
  return 1
}

# forget: deletes the matching Results, so a passing run leaves none behind.
forget() {
  for r in $(results "$@"); do
    kubectl -n "$RESULT_NAMESPACE" delete results.core.k8sgpt.ai "$r" --wait=false
  done
}
{{- end -}}
