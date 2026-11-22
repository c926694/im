const icons = {
  chat: <><path d="M20.5 11.1c0 4-3.8 7.2-8.5 7.2a10 10 0 0 1-2.6-.3L5 20l.8-3.4a6.7 6.7 0 0 1-2.3-5.5c0-4 3.8-7.2 8.5-7.2s8.5 3.2 8.5 7.2Z"/><path d="M9 11h.01m3 0h.01m3 0h.01" strokeWidth="2.5"/></>,
  contacts: <><circle cx="8" cy="7" r="3"/><path d="M2.8 19v-1.2A4.8 4.8 0 0 1 7.6 13h.8a4.8 4.8 0 0 1 4.8 4.8V19H2.8ZM16 7h5m-5 4h5m-5 4h3"/></>,
  group: <><circle cx="9" cy="7" r="3"/><path d="M2.5 19v-1a5 5 0 0 1 5-5h3a5 5 0 0 1 5 5v1h-13ZM16 4a3 3 0 0 1 0 6m1.5 3a5 5 0 0 1 4 5v1h-3"/></>,
  close: <path d="m6 6 12 12M18 6 6 18"/>,
  account: <><circle cx="12" cy="12" r="9"/><circle cx="12" cy="9" r="2.5"/><path d="M6.5 18a5.8 5.8 0 0 1 11 0"/></>,
  search: <><circle cx="10.5" cy="10.5" r="5.5"/><path d="m14.6 14.6 4.4 4.4"/></>,
  add: <><circle cx="12" cy="12" r="8.5"/><path d="M8.5 12h7M12 8.5v7"/></>,
  back: <path d="m14.5 5.5-6.5 6.5 6.5 6.5"/>,
  keyboard: <><rect x="2.5" y="5.5" width="19" height="13" rx="2"/><path d="M6.5 9.5h.01m3.66 0h.01m3.66 0h.01m3.66 0h.01M6.5 13h.01m3.66 0h.01m3.66 0h.01m3.66 0h.01M8.5 15.8h7" strokeWidth="2"/></>,
  envelope: <><rect x="3" y="5" width="18" height="14" rx="2"/><path d="m4 7 8 6 8-6"/></>,
};

export function Icon({ name }: { name: keyof typeof icons }) {
  return <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24" fill="none"
    stroke="currentColor" strokeWidth="1.65" strokeLinecap="round" strokeLinejoin="round">{icons[name]}</svg>;
}

export function Avatar({ name }: { name: string }) {
  const letters = Array.from(name);
  const tone = letters.reduce((value, letter) => (value * 31 + letter.charCodeAt(0)) % 5, 0);
  return <span className="avatar" data-tone={tone} aria-hidden="true">{letters[0].toUpperCase()}</span>;
}
