# Source of these fixtures

The `.xml` files here are responses from the BnF SRU service
(https://catalogue.bnf.fr/api/SRU), retrieved on 2026-10-11, in the
`unimarcXchange` record schema.

Contains data from the Bibliothèque nationale de France under the Licence
Ouverte / Open Licence 2.0, retrieved 2026-10-11.

Changes made for Bindery's tests:

- the text of every summary (UNIMARC field 330 $a) is replaced with a
  placeholder, because those summaries are the publisher's or a third
  party's (Electre) copy and not BnF data under the open licence;
- every 856 field (electronic location: cover image references and links to
  other catalogues) is removed, on 2026-10-11. Nothing in the package reads
  856 and Bindery fetches no BnF image, so the fixtures carry none.

Nothing else is altered. BnF does not endorse Bindery.
