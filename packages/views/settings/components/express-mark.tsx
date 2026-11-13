// eXpress ships no public brand-icon set, so this is a neutral mark: the
// letter X in a rounded speech bubble, drawn for Multica. Swap in the official
// mark if the vendor publishes one under a compatible license.
export function ExpressMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className={className} fill="currentColor">
      <path
        fillRule="evenodd"
        d="M5 2h14a3 3 0 0 1 3 3v11a3 3 0 0 1-3 3h-7.6L6.6 22.4A1 1 0 0 1 5 21.6V19a3 3 0 0 1-3-3V5a3 3 0 0 1 3-3zm3.3 4.3a1 1 0 0 0-1.4 1.4L10.6 11.4l-3.7 3.7a1 1 0 1 0 1.4 1.4l3.7-3.7 3.7 3.7a1 1 0 0 0 1.4-1.4l-3.7-3.7 3.7-3.7a1 1 0 0 0-1.4-1.4L12 10l-3.7-3.7z"
      />
    </svg>
  );
}
