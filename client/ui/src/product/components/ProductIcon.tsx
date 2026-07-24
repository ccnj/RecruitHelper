import type { SVGProps } from 'react'

export type ProductIconName =
  | 'home'
  | 'confirmation'
  | 'chat'
  | 'calendar'
  | 'interviewed'
  | 'wechat'
  | 'settings'
  | 'search'
  | 'chevron'
  | 'close'
  | 'refresh'
  | 'briefcase'
  | 'clock'
  | 'pause'
  | 'play'
  | 'copy'
  | 'user'
  | 'inbox'
  | 'warning'
  | 'check'
  | 'code'

interface ProductIconProps extends SVGProps<SVGSVGElement> {
  name: ProductIconName
  size?: number
}

const paths: Record<ProductIconName, JSX.Element> = {
  home: <><path d="M3.5 10.3 12 3l8.5 7.3" /><path d="M5.8 9.2v10h12.4v-10M9.4 19.2v-6.1h5.2v6.1" /></>,
  confirmation: <><rect x="4" y="3.5" width="16" height="17" rx="2" /><path d="M8 8h8M8 12.1l1.6 1.6 3.2-3.4M8 17h8" /></>,
  chat: <><path d="M4 5.5h16v11H9l-5 3v-14Z" /><path d="M8 9h8M8 13h5" /></>,
  calendar: <><rect x="3.5" y="5.5" width="17" height="15" rx="2" /><path d="M7.5 3v5M16.5 3v5M3.5 10h17M8 14h3M13 14h3M8 17h3" /></>,
  interviewed: <><circle cx="9" cy="8" r="3.2" /><path d="M3.8 19.5c.7-3.5 2.4-5.4 5.2-5.4 1 0 1.9.2 2.6.7" /><path d="m14.2 17.1 2 2 4.2-5" /></>,
  wechat: <><path d="M3.5 10.3c0-3.5 3.2-6.3 7.2-6.3s7.2 2.8 7.2 6.3-3.2 6.2-7.2 6.2c-.8 0-1.5-.1-2.2-.3L5 18l.9-3.1a5.9 5.9 0 0 1-2.4-4.6Z" /><path d="M13.2 14.9c.9 2.4 3.3 4.1 6 4.1.5 0 1-.1 1.4-.2l2 .9-.6-1.9a4.7 4.7 0 0 0 1.4-3.3c0-2.6-2.3-4.7-5.2-4.7h-.4" /><path d="M8.2 9.6h.1M13.1 9.6h.1M17.1 14.1h.1M20.3 14.1h.1" /></>,
  settings: <><circle cx="12" cy="12" r="3.2" /><path d="M19.4 13.3c.1-.4.1-.8.1-1.3s0-.9-.1-1.3l2-1.6-2-3.4-2.5 1a8.4 8.4 0 0 0-2.2-1.3L14.3 3h-4.1l-.4 2.4c-.8.3-1.5.7-2.2 1.3l-2.4-1-2 3.4 1.9 1.6A8 8 0 0 0 5 12c0 .5 0 .9.1 1.3l-1.9 1.6 2 3.4 2.4-1c.7.6 1.4 1 2.2 1.3l.4 2.4h4.1l.4-2.4c.8-.3 1.5-.7 2.2-1.3l2.5 1 2-3.4-2-1.6Z" /></>,
  search: <><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></>,
  chevron: <path d="m9 5 7 7-7 7" />,
  close: <path d="m6 6 12 12M18 6 6 18" />,
  refresh: <><path d="M20 8a8 8 0 1 0 .2 7.6" /><path d="M20 3v5h-5" /></>,
  briefcase: <><rect x="3" y="7" width="18" height="13" rx="2" /><path d="M8 7V4h8v3M3 12h18M10 12v2h4v-2" /></>,
  clock: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3.5 2" /></>,
  pause: <path d="M8 5v14M16 5v14" />,
  play: <path d="m8 5 11 7-11 7V5Z" />,
  copy: <><rect x="8" y="8" width="11" height="12" rx="2" /><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h2" /></>,
  user: <><circle cx="12" cy="8" r="4" /><path d="M4.5 21c.8-4.1 3.3-6.4 7.5-6.4s6.7 2.3 7.5 6.4" /></>,
  inbox: <><path d="M4 5h16l2 9v5H2v-5l2-9Z" /><path d="M2 14h6l1.4 2h5.2l1.4-2h6" /></>,
  warning: <><path d="m12 3 10 18H2L12 3Z" /><path d="M12 9v5M12 17.5h.1" /></>,
  check: <path d="m4 12 5 5L20 6" />,
  code: <path d="m9 6-6 6 6 6M15 6l6 6-6 6" />,
}

export function ProductIcon({ name, size = 20, ...props }: ProductIconProps) {
  return (
    <svg
      aria-hidden="true"
      fill="none"
      height={size}
      viewBox="0 0 24 24"
      width={size}
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.8"
      {...props}
    >
      {paths[name]}
    </svg>
  )
}

