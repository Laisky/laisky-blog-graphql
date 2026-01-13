## Requirement

This is the comment data from my personal blog. I previously used Disqus as the comment system, but later migrated to a new self-hosted comment system.

Now, I need to import the historical comment data exported from Disqus into the new database.

Add an command file in the `./cmd/` directory to provide a command-line tool for importing comments exported from Disqus. The tool should have comprehensive argument parsing, comments, and documentation. I can run `go run main.go import comments --disqus_file=disqus_exported_data.xml --db_uri=mongodb://user:pwd@addr:port/dbname` to import the comments into the specified database.

## Data Structure

The core of the blog is the articles (Posts). The data structure for articles is located at internal/web/blog/model/posts.go.

The index field for Post is post_name, which corresponds to the path of the blog article, such as /p/{post.post_name}. You can use post_name to match articles, comments, and Disqus exported comment data in our database.

## Disqus Exported Comment Structure

Disqus exported comments data is saved at: disqus_exported_data.xml

Below is the Disqus official Disqus exported comment structure

>>>
# Comments Export

*Written by Disqus — Updated over 7 years ago*

Note: exports are designed for backup purposes only and do not follow the same format as our [custom XML import format](https://help.disqus.com/customer/portal/articles/472150). Exports cannot be re-imported to other Disqus forums.

To export your comments navigate to your Disqus Admin > Community > [Export](http://disqus.com/admin/discussions/export/) and click "Export". The export will be sent into a queue and then emailed to the address associated with your account once it's ready.

**Exports may not be available for all sites, particularly those of a large size.** If you've requested an export file more than twice and still have not received a download link from us, it's likely that an export for your site is currently unavailable.

## Export Format (XML)

XML Schema Definitions are available for new-style exports. The current version can be viewed at [http://disqus.com/api/schemas/1.0/disqus.xsd](http://disqus.com/api/schemas/1.0/disqus.xsd)

Example output:

```xml
<?xml version="1.0"?>
<disqus xmlns="http://disqus.com"
        xmlns:dsq="http://disqus.com/disqus-internals"
        xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
        xsi:schemaLocation="http://disqus.com/api/schemas/1.0/disqus.xsd
                            http://disqus.com/api/schemas/1.0/internals.xsd"
>

<!--
  Categories must be declared at the top of a file. They only need
  declared once per site, and DISQUS will attempt to validate them       against existing data if they are not present in the XML.

  The `dsq` namespace is for internal usage by DISQUS and tends to
  hold things such as internal identifiers for objects.

- `title` must be unqiue per `forum`
  -->
  <category dsq:id="1">
    <forum>disqusdev</forum>
    <title>Technology</title>
  </category>

<!--
  Threads must be declared after categories, and before posts. They
  only need declared once per site, and DISQUS will attempt to
  validate them against existing data if they are not present in the
  XML.

  The `dsq` namespace is for internal usage by DISQUS and tends to
  hold things such as internal identifiers for objects.

- `id` must be unqiue per `forum`
  -->

  <thread dsq:id="2">
    <id>1</id>
    <forum>disqusdev</forum>
    <category dsq:id="1"/>
    <link/>
    <title/>
    <message/>
    <createdAt>2012-12-12T12:12:12</createdAt>
    <author>
      <name>Baz</name>
    </author>
  </thread>

<!--
  Posts must be declared in a standard tree order. Parents should
  always exist before they are referenced. DISQUS will attempt to
  validate them against existing data if they are not present in the
  XML.

  The `dsq` namespace is for internal usage by DISQUS and tends to
  hold things such as internal identifiers for objects.

- `id` must be unqiue per `forum`
  -->

  <post dsq:id="1">
    <id>2</id>
    <message>Mother Russia</message>
    <thread>1</thread>
    <isSpam>true</isSpam>
    <createdAt>2012-12-12T12:12:12</createdAt>
    <author>
      <name>Baz</name>
    </author>
  </post>
  <post dsq:id="2">
    <id>1</id>
    <message>Yo dude</message>
    <parent dsq:id="1">2</parent>
    <thread>1</thread>
    <createdAt>2012-12-12T12:12:12</createdAt>
    <author>
      <name>Baz</name>
    </author>
  </post>
  <post>
    <id>3</id>
    <message/>
    <thread dsq:id="2"/>
    <createdAt>2012-12-12T12:12:12</createdAt>
    <author>
      <name>Baz</name>
    </author>
  </post>

</disqus>
```
<<<
