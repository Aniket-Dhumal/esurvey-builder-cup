import type { LucideIcon } from "lucide-react";
import {
  Activity,
  Bot,
  ClipboardCheck,
  FileBarChart,
  FileText,
  Globe2,
  LayoutDashboard,
  Map,
  MessageSquareWarning,
  Satellite,
  Scale,
  ScrollText,
  Siren,
  SlidersHorizontal,
  UserCog,
  Users,
} from "lucide-react";

export interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
  /** Extra path prefixes that should light this item up. */
  alsoActiveFor?: string[];
  /** Light up on this exact path only, not on the paths beneath it. */
  exact?: boolean;
  /**
   * The permission this route needs. Items the signed-in officer cannot use
   * are not rendered.
   *
   * Hiding a control is a courtesy, not a control — the server checks every
   * request regardless. What it buys is a sidebar an officer can trust: a
   * Talathi who sees "Officer accounts" and gets 403 learns to distrust the
   * whole navigation.
   */
  permission?: string;

  /**
   * Hidden from the sidebar, and reachable by anyone whose permission allows
   * it.
   *
   * For the screens that belong to whoever runs the platform rather than to
   * whoever uses it. Hiding a route is not access control -- the permission
   * still is, and the server still checks it on every request -- so this only
   * decides what an officer is offered, never what they are allowed.
   */
  operatorOnly?: boolean;
}

export interface NavGroup {
  label: string;
  items: NavItem[];
}

export const NAV_GROUPS: NavGroup[] = [
  {
    label: "Overview",
    items: [
      // The client's dashboard. The product administrator holds no
      // report:read and has its own overview under Administration.
      { href: "/", label: "Dashboard", icon: LayoutDashboard, permission: "report:read" },
      {
        href: "/worklist",
        label: "My worklist",
        icon: ClipboardCheck,
        permission: "report:verify",
        alsoActiveFor: ["/verify"],
      },
      // The rung above the village pair. report:adjudicate is held by exactly
      // the Circle, the Tehsil and the District and by neither village role,
      // so it hides this from the two people whose queue is the worklist
      // above rather than this one.
      {
        href: "/approvals",
        label: "Approvals",
        icon: Scale,
        permission: "report:adjudicate",
      },
      // report:read, because reading is all the assistant does: every one of its
      // tools is a scoped store call and none of them writes. Asking for a higher
      // permission would imply it can do more than it can.
      {
        href: "/assistant",
        label: "Assistant",
        icon: Bot,
        permission: "report:read",
      },
    ],
  },
  {
    label: "Claims",
    items: [
      {
        href: "/events",
        label: "Survey events",
        icon: Siren,
        permission: "event:read",
      },
      {
        href: "/reports",
        label: "Loss reports",
        icon: FileText,
        permission: "report:read",
      },
      {
        href: "/adjudication",
        label: "Adjudication",
        icon: Scale,
        permission: "report:adjudicate",
      },
      {
        href: "/grievances",
        label: "Grievances",
        icon: MessageSquareWarning,
        permission: "grievance:read",
      },
      {
        // Oversight above a district: districts side by side, by crop, by
        // event, and the filing trend. Permissioned rather than restricted to
        // a role -- a Collector calling it gets their own district broken
        // down by taluka, which is a working comparison too.
        href: "/statewide",
        label: "Statewide",
        icon: Globe2,
        // assessment:escalate rather than report:read, which a Talathi and a
        // farmer both hold. A village authority's "statewide" view is one
        // village, and offering it is offering a page that can only
        // disappoint. The endpoint is permissioned on report:read and scoped
        // like every other read -- this decides what an officer is offered,
        // never what they are allowed.
        permission: "assessment:escalate",
      },
      {
        href: "/assessments",
        label: "Assessments",
        icon: FileBarChart,
        permission: "assessment:read",
        alsoActiveFor: ["/assessment"],
      },
    ],
  },
  {
    label: "Registry",
    items: [
      {
        href: "/farmers",
        label: "Farmers",
        icon: Users,
        permission: "farmer:read",
        alsoActiveFor: ["/farmer"],
      },
      {
        href: "/parcels",
        label: "Parcels",
        icon: Map,
        permission: "parcel:read",
      },
    ],
  },
  {
    label: "Administration",
    items: [
      {
        href: "/admin",
        label: "Officer accounts",
        icon: UserCog,
        permission: "user:read",
      },
      // The product-administrator console, and the two screens that now live
      // inside it.
      //
      // The audit trail and the data-source page are operator tools, not
      // officer tools: one is a hash-chained log read during an investigation,
      // the other says whether Earth Engine and the SAR service are answering.
      // A Tehsildar has no use for either, and "reference:read" is held by
      // nearly everybody -- so Data sources was once showing in the sidebar of
      // every officer in the state.
      //
      // They keep their own permissions and stay out of the navigation; the
      // Platform page links to them, and it is gated on platform:read, which
      // is held by whoever runs this and by nobody who uses it.
      // The product administrator's console. Counts and platform health
      // only; no client records are reachable from any of it.
      {
        href: "/platform",
        label: "Platform overview",
        icon: LayoutDashboard,
        permission: "platform:read",
        exact: true,
      },
      {
        href: "/platform/health",
        label: "Health & load",
        icon: Activity,
        permission: "platform:read",
      },
      {
        href: "/platform/integrations",
        label: "Integrations",
        icon: SlidersHorizontal,
        permission: "platform:read",
        alsoActiveFor: ["/sources"],
      },
      {
        href: "/audit",
        label: "Audit trail",
        icon: ScrollText,
        permission: "platform:read",
      },

      {
        href: "/sources",
        label: "Data sources",
        icon: Satellite,
        permission: "reference:read",
        operatorOnly: true,
      },
    ],
  },
];

/** Whether `pathname` should highlight `item`. */
export function isActive(item: NavItem, pathname: string): boolean {
  const p = pathname.replace(/\/$/, "") || "/";
  if (item.href === "/" || item.exact) return p === item.href;
  if (p === item.href || p.startsWith(item.href + "/")) return true;
  return (item.alsoActiveFor ?? []).some(
    (prefix) => p === prefix || p.startsWith(prefix + "/"),
  );
}

/** The groups this officer can actually use, empty groups dropped. */
export function visibleGroups(
  can: (permission: string) => boolean,
): NavGroup[] {
  return NAV_GROUPS.map((g) => ({
    ...g,
    items: g.items.filter(
      (i) => !i.operatorOnly && (!i.permission || can(i.permission)),
    ),
  })).filter((g) => g.items.length > 0);
}
