# Blog public publishing authorization

`BlogCreatePost` and every `BlogAmendPost` branch now require the authenticated account loaded from the existing server-side account store to satisfy `User.IsAdmin()`. The check precedes post input processing, article lookups/writes, Markdown conversion, and archive uploads. Token display fields do not establish publisher authority.

This uses the existing administrator definition without adding identities, roles, credentials, or account grants. Active-account checks continue to run first, including the existing compatibility rule for legacy Mongo accounts without a status. Active non-admin accounts retain their existing sign-in and non-publishing functions, but cannot create public posts, change article content/translations, or amend categories.

Content amendments retain their existing author ownership check: administrator status alone does not grant another author's content-editing permission. The category branch preserves the existing administrator curation capability for other authors' posts.

Authored HTML, Slide content, embeds, Markdown conversion, existing stored posts and archives, current public reads, and registered-history read authorization are unchanged. Existing trusted publisher content can remain active by design; this change establishes publisher authorization and does not claim to sanitize malicious HTML from a compromised administrator.

The retained resolver tests use dummy local signed bearers, Mongo wire mocks, and an in-memory archive adapter. Before the fix, active and legacy non-admin accounts completed all three write branches; create and content amendment each submitted one archive payload. After the fix, each is rejected with only the authoritative account lookup and no archive or article I/O. Controls cover authorized Admin writes, authored embed preservation, content ownership and existing category curation, inactive administrators, absent/invalid bearers, and missing/unavailable account stores. No real accounts, database, archive network, or blockchain transactions are used.
