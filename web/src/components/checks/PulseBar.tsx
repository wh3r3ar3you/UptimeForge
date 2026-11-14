import { Box } from '@mantine/core';
import type { Bar } from '../../api';
import { sig } from '../../theme';
import type { ChecksT } from './text';

/** barColor: grey without data, green all up, red for a down/blocked incident or mostly failing, amber otherwise. */
export function barColor(b: Bar): string {
  if (!b.n) return 'var(--uf-track)';
  if (b.worst === 'down' || b.worst === 'blocked' || b.up / b.n < 0.5) return sig.down;
  if (b.up === b.n) return sig.ok;
  return sig.warn;
}

/**
 * PulseBar is a check's last 24 hours as a row of bars, after Uptime Kuma:
 * one bar per bucket, the newest on the right. A click opens the check at
 * that time.
 */
export function PulseBar({ bars, from, step, t, onPick, compact }: {
  bars?: Bar[]; from: number; step: number; t: ChecksT; onPick?: (at: number) => void; compact?: boolean;
}) {
  if (!bars) return <Box h={compact ? 12 : 18} />;
  const fmt = (i: number) => new Date((from + i * step) * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return (
    <Box className="uf-pulse-bar" data-compact={compact || undefined}>
      {bars.map((b, i) => (
        <span
          key={i}
          style={{ background: barColor(b), ['--c' as string]: barColor(b) }}
          data-on={b.n ? '' : undefined}
          title={(b.n ? t('bar_tip', fmt(i), b.up, b.n) : t('bar_none', fmt(i))) + (b.worst ? ` · ${t('bar_incident', b.worst)}` : '')}
          onClick={onPick ? (e) => { e.stopPropagation(); onPick(from + i * step); } : undefined}
        />
      ))}
    </Box>
  );
}
