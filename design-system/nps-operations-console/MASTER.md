# NPS Operations Console Design System

This file is the visual source of truth for the embedded NPS Web console.
It applies to `web/views/` and `web/static/css/zui-console.css`.

## Product Direction

- Product: infrastructure operations console for NPS tunnels, clients, users, and server settings.
- Audience: administrators who scan state, compare records, and repeatedly configure network services.
- Character: compact, reliable, technical, and quiet. Information density takes priority over decorative effects.
- Component base: ZUI 3.0.0, vendored locally under `web/static/`; do not introduce a CDN dependency for console UI.

## Foundations

### Color Tokens

| Role | Light | Dark |
| --- | --- | --- |
| Canvas | `#F4F7FB` | `#0F1726` |
| Surface | `#FFFFFF` | `#172235` |
| Primary action | `#1E40AF` | `#80A9FF` |
| Primary hover | `#1D4ED8` | `#A8C4FF` |
| Text | `#172033` | `#EDF3FC` |
| Muted text | `#5F6F85` | `#AAB8CB` |
| Divider | `#DBE4EF` | `#2A3B54` |
| Success | `#16794C` | `#65D19B` |
| Warning | `#A46105` | `#F2BF71` |
| Danger | `#B42332` | `#FF9DA6` |

Use the `--nps-*` semantic tokens from `zui-console.css`; do not add raw per-page colors.
Status is always expressed with both color and a text label or icon.

### Typography And Spacing

- UI font stack: `Fira Sans`, `Noto Sans SC`, `Microsoft YaHei`, then the system sans-serif font.
- Code, client keys, and command examples use the existing monospace treatment only where values must be copied or compared.
- Use a 4px / 8px rhythm. Standard gaps are 8px, 12px, 16px, 24px, and 32px.
- Console text is intentionally compact at 14px on desktop. Labels and helper text remain legible at 12px or above.
- Use normal letter spacing for prose and labels. Numeric values should remain stable when data refreshes.

### Shape And Elevation

- Panels, fields, menus, and buttons use 6px to 8px radii.
- Surfaces use a thin divider plus the small shared shadow; avoid floating-card stacks and oversized elevation.
- Never use decorative gradients, colored glow, or glass blur as a substitute for hierarchy.

## Shell

- Desktop uses a persistent 256px navigation rail and a 60px fixed utility bar.
- The active navigation item has a restrained blue inset indicator, stronger text, and an icon color change.
- Navigation icons use the ZUI ZenIcon font and must be `aria-hidden` when their adjacent label names the destination.
- Keep primary navigation, language control, theme switch, and logout reachable on every management page.
- Main content reserves space for the fixed bar with `scroll-padding-top` and wrapper padding so keyboard focus is not obscured.

## Components

### Panels And Dashboard

- Use ZUI `panel` semantics together with the legacy `ibox` hooks required by existing behavior.
- Dashboard cards use one thin left status accent, not a full saturated card background.
- Keep chart labels, titles, and state text visible; charts must not communicate state by color alone.

### Tables And Lists

- A list has one quiet management surface: command toolbar, readable column header, row hover, and pagination.
- Preserve horizontal table access where necessary instead of clipping operational data.
- Keep destructive actions visually distinct from regular controls and retain existing confirmations.
- Empty states explain the absence of records and keep the creation action discoverable.

### Forms

- Every field keeps a visible label, a focused blue outline, and persistent helper text for operational inputs.
- Group advanced settings under concise section titles rather than placing all fields in a single undifferentiated block.
- Primary submit is blue. Success, warning, and destructive actions retain their semantic colors.
- Preserve native input controls and the existing validation flow; do not replace accessible controls with decorative divs.

### Authentication

- Login and registration use the same neutral canvas, white surface, blue primary action, and visible labels as the console.
- Password visibility and language controls remain named buttons with clear focus feedback.

## Responsive And Accessibility Rules

- Test at 375px, 768px, 1024px, and a wide desktop viewport. Do not permit page-level horizontal overflow on mobile.
- At tablet widths, reduce the rail and content gutters. At mobile widths, prioritize main content and expose the compact navigation control.
- Keep form controls, primary actions, and top-bar controls comfortably tappable on small screens.
- Respect `prefers-reduced-motion`; transitions become effectively instant when the user requests reduced motion.
- Maintain visible keyboard focus, logical heading order, and labels for icon-only controls.
- Do not rely on hover for an essential action. Use transitions only for state feedback, never to move layout boundaries.

## Implementation Boundaries

- Load `zui-3.0.0.css` and `zui-3.0.0.js` locally before legacy jQuery so ZUI does not overwrite jQuery's `$` alias.
- Load `zui-console.css` after the legacy visual styles. It provides the token layer and compatibility overrides.
- Keep existing NPS data, modal, and controller contracts intact while using ZUI primitives for the rendered controls; do not reintroduce Bootstrap assets or plugins.
- Retain the ZUI 3 MIT license notice in `web/static/licenses/zui-3.0.0-MIT.txt`.
