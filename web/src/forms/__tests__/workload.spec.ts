// 工作负载表单 parse/build 往返测试：守护可视化编辑与 YAML 的一致性
import { describe, it, expect } from 'vitest'
import { parseWorkload, buildWorkload } from '../workload'

const deployment = {
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: { name: 'demo', namespace: 'prod', labels: { app: 'demo' } },
  spec: {
    replicas: 3,
    selector: { matchLabels: { app: 'demo' } },
    minReadySeconds: 5,
    revisionHistoryLimit: 3,
    progressDeadlineSeconds: 300,
    template: {
      metadata: { labels: { app: 'demo' } },
      spec: {
        initContainers: [{ name: 'init', image: 'busybox:1.36', command: ['sh', '-c', 'echo ok'] }],
        containers: [
          {
            name: 'web',
            image: 'nginx:1.27',
            imagePullPolicy: 'IfNotPresent',
            workingDir: '/app',
            env: [
              { name: 'A', value: '1' },
              { name: 'NS', valueFrom: { fieldRef: { fieldPath: 'metadata.namespace' } } },
              { name: 'DB', valueFrom: { secretKeyRef: { name: 'db-secret', key: 'password' } } },
              { name: 'CFG', valueFrom: { configMapKeyRef: { name: 'app-cfg', key: 'mode' } } },
            ],
            envFrom: [{ prefix: 'P_', configMapRef: { name: 'cfg' } }],
            ports: [{ name: 'http', containerPort: 8080, hostPort: 8080, protocol: 'TCP' }],
            resources: {
              requests: { cpu: '100m', memory: '128Mi', 'ephemeral-storage': '2Gi' },
              limits: { cpu: '500m', memory: '256Mi' },
            },
            readinessProbe: { httpGet: { path: '/ready', port: 8080 }, initialDelaySeconds: 3, periodSeconds: 5 },
            startupProbe: { tcpSocket: { port: 8080 } },
            lifecycle: {
              postStart: { exec: { command: ['touch', '/tmp/ok'] } },
              preStop: { httpGet: { path: '/stop', port: 8080 } },
            },
            securityContext: { runAsUser: 1000, runAsNonRoot: true, readOnlyRootFilesystem: true, capabilities: { add: ['NET_ADMIN'], drop: ['ALL'] } },
            volumeMounts: [{ name: 'data', mountPath: '/data' }],
          },
        ],
        volumes: [{ name: 'data', emptyDir: {} }],
        imagePullSecrets: [{ name: 'harbor-secret' }],
        dnsPolicy: 'ClusterFirst',
        terminationGracePeriodSeconds: 45,
        hostAliases: [{ ip: '10.0.0.1', hostnames: ['db.local', 'cache.local'] }],
        securityContext: { runAsUser: 1000, fsGroup: 2000, runAsNonRoot: true },
        affinity: {
          nodeAffinity: {
            requiredDuringSchedulingIgnoredDuringExecution: {
              nodeSelectorTerms: [{ matchExpressions: [{ key: 'kubernetes.io/os', operator: 'In', values: ['linux'] }] }],
            },
          },
        },
      },
    },
  },
}

describe('workload parse', () => {
  const d = parseWorkload(deployment, 'deployments')
  const c = d.containers[0]

  it('环境变量 valueFrom 各来源解析', () => {
    expect(c.envList.some((e: any) => e.sourceType === 'secretKeyRef' && e.refName === 'db-secret')).toBe(true)
    expect(c.envList.some((e: any) => e.sourceType === 'configMapKeyRef' && e.key === 'mode')).toBe(true)
    expect(c.envList.some((e: any) => e.sourceType === 'fieldRef' && e.fieldPath === 'metadata.namespace')).toBe(true)
    expect(c.envList.some((e: any) => e.sourceType === 'value' && e.value === '1')).toBe(true)
  })
  it('envFrom 解析', () => {
    expect(c.envFrom[0]).toMatchObject({ type: 'configMap', name: 'cfg', prefix: 'P_' })
  })
  it('端口 hostPort', () => expect(c.ports[0].hostPort).toBe(8080))
  it('安全上下文', () => {
    expect(c.sc.runAsUser).toBe(1000)
    expect(c.sc.readOnlyRootFilesystem).toBe(true)
    expect(c.sc.capAdd).toContain('NET_ADMIN')
  })
  it('启动探针 / 生命周期', () => {
    expect(c.startupProbe.enabled).toBe(true)
    expect(c.startupProbe.type).toBe('tcpSocket')
    expect(c.preStop.enabled).toBe(true)
    expect(c.preStop.type).toBe('httpGet')
  })
  it('Pod 级字段', () => {
    expect(d.imagePullSecrets[0].name).toBe('harbor-secret')
    expect(d.podSc.fsGroup).toBe(2000)
    expect(d.hostAliases[0].hostnamesText).toBe('db.local, cache.local')
    expect(d.nodeAffinityRows[0].key).toBe('kubernetes.io/os')
    expect(d.terminationGracePeriodSeconds).toBe(45)
  })
})

describe('workload build', () => {
  it('Deployment 往返无损', () => {
    const d = parseWorkload(deployment, 'deployments')
    const built = buildWorkload(deployment, d, 'deployments')
    const wc = built.spec.template.spec.containers[0]
    expect(wc.env).toEqual(deployment.spec.template.spec.containers[0].env)
    expect(wc.securityContext).toEqual(deployment.spec.template.spec.containers[0].securityContext)
    expect(wc.lifecycle.postStart.exec.command).toEqual(['touch', '/tmp/ok'])
    expect(wc.lifecycle.preStop.httpGet.path).toBe('/stop')
    expect(wc.ports[0].hostPort).toBe(8080)
    expect(built.spec.template.spec.imagePullSecrets[0].name).toBe('harbor-secret')
    expect(built.spec.template.spec.securityContext.fsGroup).toBe(2000)
    expect(built.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key).toBe('kubernetes.io/os')
    expect(built.spec.minReadySeconds).toBe(5)
    expect(built.spec.revisionHistoryLimit).toBe(3)
  })
  it('StatefulSet 卷声明模板与更新策略', () => {
    const sts = {
      apiVersion: 'apps/v1', kind: 'StatefulSet', metadata: { name: 's' },
      spec: {
        serviceName: 'svc', podManagementPolicy: 'Parallel',
        updateStrategy: { type: 'RollingUpdate', rollingUpdate: { partition: 2 } },
        volumeClaimTemplates: [{ metadata: { name: 'data' }, spec: { storageClassName: 'nfs', accessModes: ['ReadWriteOnce'], resources: { requests: { storage: '10Gi' } } } }],
        template: { spec: { containers: [{ name: 'a', image: 'b' }] } },
      },
    }
    const d = parseWorkload(sts, 'statefulsets')
    expect(d.volumeClaimTemplates[0].size).toBe('10Gi')
    expect(d.updatePartition).toBe(2)
    const built = buildWorkload(sts, d, 'statefulsets')
    expect(built.spec.volumeClaimTemplates[0].spec.resources.requests.storage).toBe('10Gi')
    expect(built.spec.updateStrategy.rollingUpdate.partition).toBe(2)
  })
})
