import { Route, Routes } from 'react-router-dom'
import { AppShell } from '@/components/app-shell'
import { ClusterPage } from '@/pages/cluster'
import { Placeholder } from '@/pages/placeholder'

export default function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<ClusterPage />} />
        <Route path="nodes" element={<Placeholder title="Nodes" />} />
        <Route path="nodes/:id" element={<Placeholder title="Node detail" />} />
        <Route path="projects" element={<Placeholder title="Projects" />} />
        <Route path="deploy" element={<Placeholder title="Deploy" />} />
        <Route path="events" element={<Placeholder title="Events" />} />
        <Route path="*" element={<Placeholder title="Not found" />} />
      </Route>
    </Routes>
  )
}
