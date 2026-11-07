import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'

interface Props {
  children: ReactNode
  // Changing this (the page route) clears a shown error.
  resetKey?: string
}

interface State {
  error: Error | null
}

/** Shows what broke instead of a blank screen, and lets the user try again. */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Page crashed:', error, info.componentStack)
  }

  componentDidUpdate(prev: Props) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) this.setState({ error: null })
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div role="alert" className="flex max-w-[640px] flex-col gap-3 rounded-[10px] border border-border bg-card p-6">
        <h2 className="m-0 text-lg font-semibold">This page hit a problem</h2>
        <p className="m-0 font-mono text-[13px] text-bad-fg">{this.state.error.message}</p>
        <div>
          <Button onClick={() => this.setState({ error: null })}>Try again</Button>
        </div>
      </div>
    )
  }
}
