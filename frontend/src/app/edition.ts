/**
 * Build edition. The public build is compiled without the owner workspaces: the
 * constant is replaced at build time, so their pages, models and ports are dropped
 * from the bundle instead of being hidden behind a runtime check.
 *
 * Call sites that must disappear from the public bundle use `__OWNER_EDITION__`
 * directly, because only a literal at the branch itself lets the bundler remove the
 * module it references.
 */
export const OWNER_EDITION: boolean = __OWNER_EDITION__
