import { Route, Routes } from 'react-router-dom'
import { AppShell } from '@/components/app-shell'
import { ClusterPage } from '@/pages/cluster'
import { DashboardPage } from '@/pages/dashboard'
import { DeployPage } from '@/pages/deploy'
import { EventsPage } from '@/pages/events'
import { Placeholder } from '@/pages/placeholder'
import { NodePage } from '@/pages/node'
import { NodesPage } from '@/pages/nodes'
import { ProjectPage } from '@/pages/project'
import { ServicePage } from '@/pages/service'
import { ProjectsPage } from '@/pages/projects'

export default function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<DashboardPage />} />
        <Route path="cluster" element={<ClusterPage />} />
        <Route path="nodes" element={<NodesPage />} />
        <Route path="nodes/:id" element={<NodePage />} />
        <Route path="projects" element={<ProjectsPage />} />
        <Route path="projects/:name" element={<ProjectPage />} />
        <Route path="projects/:name/services/:service" element={<ServicePage />} />
        <Route path="deploy" element={<DeployPage />} />
        <Route path="events" element={<EventsPage />} />
        <Route path="*" element={<Placeholder title="Not found" />} />
      </Route>
    </Routes>
  )
}
