import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { deriveNodes } from '@/lib/cluster'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { AddNodeDialog } from '@/components/add-node-dialog'
import { Topology } from '@/components/topology/topology'

const POLL_MS = 3000

// "192.168.1.20" -> "192.168.1.0/24". The control plane does not report its
// subnet, so a /24 around the host is the usual home or lab network.
function networkLabel(address: string | undefined): string {
  const m = address?.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3})\.\d{1,3}$/)
  return m ? `Network ${m[1]}.0/24` : 'Local network'
}

export function ClusterPage() {
  const [adding, setAdding] = useState(false)
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: POLL_MS })
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: POLL_MS })
  // Discovery reads the host's ARP table. If the control plane is older or
  // cannot read it, the page simply shows no discovered devices.
  const discoveredQ = useQuery({ queryKey: ['discovered'], queryFn: api.discovered, refetchInterval: 60000, retry: false })

  const nodes = useMemo(() => deriveNodes(nodesQ.data ?? [], workloadsQ.data ?? []), [nodesQ.data, workloadsQ.data])
  const discovered = discoveredQ.data?.devices ?? []
  // Why the list is empty, when it is fixable: an old control plane (404) or
  // the OS hiding the network from it (the server's own hint).
  const discoveryNote = discoveredQ.isError
    ? discoveredQ.error.message.startsWith('404')
      ? 'Device discovery needs a newer control plane. Reinstall and restart it.'
      : `Device discovery failed: ${discoveredQ.error.message}`
    : discoveredQ.data?.hint

  const schedulable = nodes.filter((n) => n.state === 'available' || n.state === 'throttling').length
  const workloads = (workloadsQ.data ?? []).filter((w) => w.DesiredState === 'running')
  const failed = workloads.filter((w) => w.Status === 'failed').length
  const host = nodes.find((n) => n.isHost) ?? nodes[0]

  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="m-0 text-2xl font-semibold tracking-tight">Cluster topology</h1>
          <p className="m-0 mt-1 text-muted-foreground">
            {nodes.length} nodes · {schedulable} schedulable · {workloads.length} workloads · {failed} failed. Hover a machine for
            details, click to open it.
          </p>
        </div>
        <Button asChild>
          <Link to="/deploy">New deploy</Link>
        </Button>
      </div>

      {nodesQ.isError && (
        <p role="alert" className="m-0 rounded-md border border-bad/30 bg-bad-soft p-3 text-sm text-bad-fg">
          Cannot reach the control plane: {nodesQ.error.message}. Is it running on port 8099?
        </p>
      )}

      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-x-5 gap-y-2 border-b border-border px-4 py-3 text-xs text-muted-foreground">
          <span className="text-sm font-medium text-foreground">
            {networkLabel(host?.address)}
            {discovered.length > 0 && (
              <span className="ml-2 text-xs font-normal text-muted-foreground">
                {discovered.length} {discovered.length === 1 ? 'device' : 'devices'} seen, not joined
                {discovered.length > 4 && ' (4 shown)'}
              </span>
            )}
          </span>
          <span className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
            <Legend color="bg-ok" label="available" />
            <Legend color="bg-warn" label="throttling" />
            <Legend color="bg-off" label="cordoned" />
            <Legend color="bg-bad" label="no heartbeat" />
            <span className="flex items-center gap-1.5">
              <span aria-hidden="true" className="size-2 rounded-full border border-dashed border-off" />
              discovered, not joined
            </span>
          </span>
        </div>
        {discoveryNote && (
          <p role="status" className="m-0 border-b border-border bg-warn-soft px-4 py-2 text-xs text-warn-fg">
            {discoveryNote}
          </p>
        )}
        <Topology nodes={nodes} discovered={discovered} onAddNode={() => setAdding(true)} />
      </Card>

      <AddNodeDialog open={adding} onOpenChange={setAdding} />
    </main>
  )
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <span className="flex items-center gap-1.5">
      <span aria-hidden="true" className={`size-2 rounded-full ${color}`} />
      {label}
    </span>
  )
}
