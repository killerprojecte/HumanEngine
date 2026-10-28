# Known memes versus usable memes

## Admission and recognition

Incoming live, nonbot stickers can enter the library even when scene learning is
off. Automatic admission marks them **known for understanding**, not usable for
sending or sharing; an administrator can deliberately include them later.
An unavailable vision route does not prevent byte-backed admission: the record
stays known and unanalyzed. History reads may recognize existing records but do
not collect new assets or replay live ingestion.

Admission uses structured platform hints adjacent to the actual media part:
`mface`, `sticker`, `emoji`, marketplace image markers, and the conservative QQ
image subtype subset 1/7. A summary alone, arbitrary user text, ordinary photos,
and unknown subtypes do not establish sticker admission. Ordinary images may
reuse an already-visible exact content match, but unknown photos are not added.
The platform classification is a transport hint, never proof of image meaning.

Primary implementations consulted:

- [NapCat message-segment documentation](https://doc.napneko.icu/onebot/segment)
  documents marketplace images and their extra emoji identifiers.
- [MaiBot SnowLuma image conversion](https://github.com/Mai-with-u/MaiBot-SnowLuma-Adapter/blob/main/codecs/inbound/message_codec.py)
  distinguishes image subtypes from expressive images. HumanEngine independently
  uses a narrower explicit subtype set rather than treating every unfamiliar
  subtype or nonempty caption as a sticker. No upstream code was copied.

## Content identity and scene isolation

`contentHash` is SHA-256 of validated original bytes, independent of download URL
or internal asset ID. New private assets use a scene-scoped identifier, so matching
a foreign private image does not import that scene's tags, availability, sources,
or sharing decision. Existing legacy records without `contentHash` retain their
original digest interpretation. Byte files are checked against the content hash,
including an existing-file collision.

Recognition requires a valid source in the current scene, or an explicitly
reviewed shared asset with a valid source. Recalled local sources cannot borrow
validity from another scene. Known records never appear in send-tool search,
original-image retrieval for sending, or automatic sharing, even if an inconsistent
stored flag says available. Existing usable records are not downgraded simply
because the current scene has learning disabled.

## Shared analysis and review

Background intake and current-turn recognition use the same scene/content
single-flight analysis and existing bounded multiframe tag extractor. Stored
emotion, tone, intent, meme meaning and OCR are reused as attributed, untrusted
observations; they are not claims about the sender's private feelings. Duplicate
URLs do not cause a second analysis of already-analyzed content. Subscriber
cancellation does not cancel another active observer. A short same-source failure
cooldown prevents the background and turn paths from repeatedly retrying the same
failed analysis.

Learning-on admission follows existing conservative selection rules for newly
analyzed assets. Previously analyzed known assets do not become usable merely
because learning is later enabled or analysis is forced again. Promotion requires
an explicit administrator action, independent of the learning switch. Generic flag
edits cannot bypass known-only admission. The dashboard exposes Known and Included
states, confirmed inclusion/removal actions, and confirmed permanent deletion.
Removing inclusion preserves bytes and labels but immediately removes sending
eligibility and sharing. Including an analyzed, valid-source record does not
automatically share it. Deletion removes that scoped record and its files; it
does not remove independent records with the same content hash.

The optional JSON fields preserve old stored rows without a destructive table
migration. Backup and restore retain scoped filenames and verify original bytes
against `contentHash`, falling back to legacy IDs; manifest hashes alone cannot
substitute for the database content identity. Tests use synthetic bytes and fake model outputs to verify persistence,
isolation, source authority, concurrency, cancellation, learning toggles and model
request counts. They do not measure real-world meme interpretation accuracy.
