/**
 * Single source of truth for links and product facts used across the site.
 *
 * OctoPort is a hosted service: we run the control plane, and users only ever
 * install a client. Nothing here should describe server-side operation.
 */

export const GITHUB_URL = 'https://github.com/047pegasus/octoport';
export const RELEASES_URL = `${GITHUB_URL}/releases`;

/** Public endpoints of the hosted service. */
export const SITE_ORIGIN = 'https://octoport.itanishq.space';
export const CONTROL_PLANE_ORIGIN = 'https://octoport-control-plane.itanishq.space';

export const INSTALL_URL = `${SITE_ORIGIN}/install.sh`;
export const INSTALL_CMD = `curl -sL ${INSTALL_URL} | sh`;

/** Tunnels are published as <random>.<TUNNEL_DOMAIN>. */
export const TUNNEL_DOMAIN = 'itanishq.space';
export const EXAMPLE_URL = `https://k7xq2p9m.${TUNNEL_DOMAIN}`;

/** Primary navigation — Docs, GitHub, Download. */
export const NAV = [
  { href: '/docs', label: 'Docs', external: false },
  { href: GITHUB_URL, label: 'GitHub', external: true },
  { href: '/download', label: 'Download', external: false },
] as const;

/** Account limits enforced by the hosted service. */
export const LIMITS = {
  idleTimeout: '5 minutes',
  maxLifetime: '36 hours',
  concurrentTunnels: 5,
  subdomainChars: 8,
} as const;
