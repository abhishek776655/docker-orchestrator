import { Card } from '@/components/ui/card'

export function Placeholder({ title }: { title: string }) {
  return (
    <main className="mx-auto max-w-[1180px] p-6">
      <h1 className="m-0 text-2xl font-semibold tracking-tight">{title}</h1>
      <Card className="mt-6 p-6 text-muted-foreground">This page is not built yet. The cluster page comes first.</Card>
    </main>
  )
}
