import { describe, expect, it } from 'vitest'
import type { ApiWorkload } from './api'
import { deriveProjects } from './cluster'

function wl(project: string, service: string, desired: 'running' | 'stopped', status: string): ApiWorkload {
  return {
    Project: project,
    Service: service,
    Revision: 'latest',
    Image: '',
    Resources: { CPUMillis: 0, MemoryBytes: 0 },
    DesiredState: desired,
    Status: status,
    StatusReason: '',
    NodeID: 'n',
  }
}

describe('deriveProjects', () => {
  it('keeps fully stopped projects visible so they can be found and removed', () => {
    const projects = deriveProjects([
      wl('cancel', 'api', 'running', 'ready'),
      wl('dockerizelocalhost', 'api', 'stopped', 'stopped'),
      wl('dockerizelocalhost', 'db', 'stopped', 'stopped'),
    ])
    expect(projects.map((p) => p.name)).toEqual(['cancel', 'dockerizelocalhost'])
    expect(projects[1].statusCounts).toEqual([{ status: 'stopped', count: 2 }])
  })
})
