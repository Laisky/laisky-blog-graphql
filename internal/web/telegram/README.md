# Telegram Push Bot

![telegram-bot](https://s1.laisky.com/uploads/2019/10/telegram-bots-father.png)

you can use this bot to create your own alert channel,
then you can push text message to GraphqlAPI,
everyone that has joint this channel will receive what you pushed.

This is a side-project **JUST FOR FUN**,
this service will keep running for a long time,
but does not provide any guarantees.

Summary:

- bot name: `laisky_alert_bot`
- Repo: <https://github.com/Laisky/laisky-blog-graphql/tree/master/telegram>
- GraphQL UI: <https://blog.laisky.com/graphql/ui/>
- GraphQL API: <https://blog.laisky.com/graphql/query/>

## Usage

Step:

1. add bot
2. create new channel
3. push msg to GraphQL API

Other methods:

- list all channels
- refresh channel's join_key and push_token
- quit a channel
- kick someone out of a channel

### Add Bot

Search `laisky_alert_bot`:

![telegram-bot](https://s3.laisky.com/uploads/2019/10/bot-1.jpg)

### Create new channel

1. `/monitor`
2. `1 - <your_new_channel_name>`

![telegram-bot](https://s2.laisky.com/uploads/2019/10/bot-2.jpg)

you will get your new channel's:

- `name`
- `push_token`
- `join_key`

### List all channels you have joint

1. `/monitor`
2. `2`

![telegram-bot](https://s2.laisky.com/uploads/2019/10/bot-3.jpg)

### Push msg to a channel

this operation will notify everyone in this channel.

open GraphQL UI: <https://blog.laisky.com/graphql/ui/>

```js
mutation push_msg {
  TelegramMonitorAlert(
    type: "hello",
    token: "hrffbxeFNjwTGIienoIE"
    msg: "hello, JEDI"
  ) {
    name
  }

```

![telegram-bot](https://s2.laisky.com/uploads/2019/10/bot-4.jpg)

receive msg:

![telegram-bot](https://s2.laisky.com/uploads/2019/10/bot-10.jpg)

### Quit a channel

1. `/monitor`
2. `5 - <channel_name>`

![telegram-bot](https://s2.laisky.com/uploads/2019/10/bot-5.jpg)

### Join a channel

1. `/monitor`
2. `3 - <channel_name>:<join_key>`

![telegram-bot](https://s3.laisky.com/uploads/2019/10/bot-6.jpg)

### Kick someone out of a channel

this operation will notify everyone in this channel.

1. `/monitor`
2. `6 - <channel_name>:<user_telegram_id>`

![telegram-bot](https://s3.laisky.com/uploads/2019/10/bot-7.jpg)

You can use GraphQL API to load all users that in this channel:

```js
query telegram {
  TelegramAlertTypes(
    name: "hello",
  ) {
    name
    sub_users {
      name
      telegram_id
    }
  }
}
```

![telegram-bot](https://s3.laisky.com/uploads/2019/10/bot-8.jpg)

### Refresh channel's join_key and push_token

this operation will notify everyone in this channel.

1. `/monitor`
2. `4 - <channel_name>`

![telegram-bot](https://s3.laisky.com/uploads/2019/10/bot-9.jpg)
