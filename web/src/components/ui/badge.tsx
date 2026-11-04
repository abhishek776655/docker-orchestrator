import type { ComponentProps } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium',
  {
    variants: {
      tone: {
        ok: 'bg-ok-soft text-ok-fg',
        warn: 'bg-warn-soft text-warn-fg',
        bad: 'bg-bad-soft text-bad-fg',
        off: 'bg-off-soft text-off-fg',
        info: 'bg-info-soft text-info-fg',
      },
    },
    defaultVariants: { tone: 'off' },
  },
)

export type Tone = NonNullable<VariantProps<typeof badgeVariants>['tone']>

export function Badge({ className, tone, ...props }: ComponentProps<'span'> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ tone }), className)} {...props} />
}
