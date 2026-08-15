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
export const INSTALL_UNINSTALL_CMD = `curl -sL ${INSTALL_URL} | sh -s -- --uninstall`;

/** Windows installs run through PowerShell (no git/bash on PATH). */
export const INSTALL_PS1_URL = `${SITE_ORIGIN}/install.ps1`;
export const INSTALL_PS1_CMD = `iex (irm ${INSTALL_PS1_URL})`;
export const INSTALL_PS1_UNINSTALL_CMD = `& ([scriptblock]::Create((irm ${INSTALL_PS1_URL}))) --uninstall`;

/** Tunnels are published as <random>.<TUNNEL_DOMAIN>. */
export const TUNNEL_DOMAIN = 'itanishq.space';
export const EXAMPLE_URL = `https://k7xq2p9m.${TUNNEL_DOMAIN}`;

/**
 * Primary navigation. GitHub is deliberately absent: it is rendered
 * separately in the nav with its own mark, since it leaves the site.
 */
export const NAV = [
  { href: '/docs', label: 'Docs', external: false },
  { href: '/download', label: 'Download', external: false },
] as const;

/** Account limits enforced by the hosted service. */
export const LIMITS = {
  idleTimeout: '5 minutes',
  maxLifetime: '36 hours',
  concurrentTunnels: 5,
  subdomainChars: 8,
} as const;
