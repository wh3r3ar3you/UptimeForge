import { useId } from 'react';

/** TelegramLogo is the round brand mark. Tabler only has the outlined plane, which reads as "send". */
export function TelegramLogo({ size = 20 }: { size?: number; stroke?: number }) {
  const id = useId();
  return (
    <svg width={size} height={size} viewBox="0 0 240 240" aria-hidden style={{ flex: 'none' }}>
      <defs>
        <linearGradient id={id} x1="120" y1="0" x2="120" y2="240" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#37aee2" />
          <stop offset="1" stopColor="#1e96c8" />
        </linearGradient>
      </defs>
      <circle cx="120" cy="120" r="120" fill={`url(#${id})`} />
      <path fill="#c8daea" d="M98 175c-3.888 0-3.227-1.468-4.568-5.17L82 132.207 170 80" />
      <path fill="#a9c9dd" d="M98 175c3 0 4.325-1.372 6-3l16-15.558-19.958-12.035" />
      <path fill="#f6fbfe" d="M100.04 144.41l48.36 35.729c5.519 3.045 9.501 1.468 10.876-5.123l19.685-92.763c2.015-8.08-3.08-11.746-8.36-9.349L54.012 117.475c-7.89 3.165-7.843 7.567-1.438 9.528l29.663 9.259 68.673-43.325c3.242-1.966 6.218-.91 3.776 1.258" />
    </svg>
  );
}
