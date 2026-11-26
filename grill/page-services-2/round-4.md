# Autorisation sans stockage, ordre des agents : tour 4

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris autre chose (réponse, objection, commentaire) sous n'importe quelle question, puis Envoyer.

Deux sujets que tu as remontés en essayant la visionneuse de sdot, relayés par sa session.

---

## Q1 : la fenêtre reçoit la description au moment de l'ouvrir, et rien n'est gardé

Aujourd'hui, un refus garde la description une heure en mémoire (`pending`), sous un id mis dans le lien `authorize`. Ta proposition supprime ce stockage :

1. Le fragment du refus contient un **formulaire** `method="post" target="_blank"` vers `http://127.0.0.1:<port d'errand>/authorize`. Il porte en champs cachés la description (son JSON), `on` (l'id de la machine) et la vue (son id). Son bouton s'intitule « Set it up on lmo… ». La page n'a toujours aucun code à écrire : elle insère `html`.
2. errand reçoit ce POST et vérifie trois choses :
   - l'origine est celle des vues (le port des stores, sur 127.0.0.1 ou localhost) ;
   - son cookie est présent ;
   - la vue est connue.

   Il relit alors la description, la résout sur la machine et affiche la fenêtre. **Rien n'est écrit.**
3. Le bouton **Authorize** de la fenêtre renvoie la description elle-même, avec la même origine et le même cookie que les actions de la page d'errand aujourd'hui. errand recalcule le hash et autorise.

Est-ce sûr ?

- **Le cookie** est `SameSite=Strict`. Depuis la vue, qui est sur le même site (127.0.0.1, le port ne compte pas), il est envoyé. Depuis un autre site, il ne l'est pas, et errand ne montre rien.
- **Un autre compte** de la machine n'a pas le cookie.
- **La fenêtre n'est jamais dans une iframe**, donc personne ne peut faire cliquer « Authorize » à l'insu de la personne.
- **Le risque reste celui d'aujourd'hui** : une page peut faire afficher n'importe quelle description, mais rien n'est autorisé sans le clic, la commande entière sous les yeux.

`POST .errand/srv` continue de donner `authorize`, sans id : l'adresse où poster le formulaire, pour une page qui dessine elle-même. Un lien seul (GET) ne suffit plus, puisque la description voyage dans le corps.

➡️ **Recommandé :** oui. Le formulaire POST dans le fragment, l'origine des vues vérifiée à l'arrivée, Authorize qui renvoie la description, et `pending` supprimé.

**Réponse :**

---

## Q2 : toujours un lien « Set it up », sauf pour une description illisible

Aujourd'hui, l'état `unavailable` n'a pas de lien. Or c'est souvent quelque chose que la fenêtre peut régler :

- **pas de checkout pour `source.dev`** : la fenêtre propose de le déclarer. Elle donne le dépôt, un champ pour le chemin du checkout sur la machine et un autre pour l'env, et montre la commande équivalente `errand serve dev REPO PATH --env ENV`. Un bouton « Declare this checkout » l'écrit dans `~/.config/errand/config.json` de cette machine, puis la fenêtre résout de nouveau ;
- **pas d'outil, et rien à télécharger pour cette plateforme** : le champ de chemin de l'outil suffit souvent ;
- **trop pour la machine** (cartes, mémoire) : la fenêtre le dit, sans rien à régler.

Seule une description refusée à la lecture (une clé inconnue, un joker inconnu) n'a pas de lien : c'est le producteur de la page qui doit la corriger.

➡️ **Recommandé :** oui. Le lien est toujours là sauf pour une description illisible, et la fenêtre sait déclarer un checkout (chemin, env, la commande montrée, un clic).

**Réponse :**

---

## Q3 : l'ordre des agents d'une machine

Aujourd'hui, un agent qui démarre retire tous ceux dont le `Release` n'est pas plus grand que le sien (`retireOthers`, `internal/agent/place.go`). À `Release` égal, c'est le dernier binaire appelé qui prend la place. Un ancien binaire encore appelé quelque part (une copie dans `~/.cache/errand/bin`, un autre env, un chemin écrit dans un script) reprend donc la place au plus récent, et deux binaires peuvent se la renvoyer.

Un ordre existe déjà pour le nom stable (`versionBefore`, qui compare `2026.11.25.1 < 2026.11.25.1+dev.1.g3e4580c < 2026.11.25.2`). Mais deux builds successives d'un arbre modifié portent la même chaîne (`0+dev.4.geae2da1.dirty`), et rien ne les départage.

Proposition :

- **Un tampon de build.** `packaging/backend.py` ajoute `-X system.Built=<instant de la build, en ns>` à côté de `system.Version`. Un `go build` à la main donne 0, comme une build de test.
- **L'ordre** est `Release`, puis la version (`versionBefore`), puis `Built`.
- **Un agent ne retire que ceux qui sont avant lui.** Un binaire plus ancien qui est appelé ne démarre pas d'agent : à `Release` égal, il parle à l'agent en place, qui parle le même protocole ; à `Release` plus petit, on lui dit de se mettre à jour, comme aujourd'hui.
- **Deux builds exactement égales selon cet ordre** (impossible avec le tampon, sauf deux builds à 0) : celle qui est en place la garde. Plus de « le dernier appelé gagne ».

Ta piste d'une liste des hashes qu'une version peut remplacer marcherait aussi. Il faudrait la tenir à jour à chaque build et la transporter avec le binaire, alors qu'un tampon dit la même chose sans rien tenir.

➡️ **Recommandé :** oui. Un tampon de build, l'ordre `Release`, puis version, puis `Built`, et seul ce qui est avant est retiré. Un binaire plus ancien utilise l'agent en place au lieu de le remplacer.

**Réponse :**

---

## Q4 : une build de test, ou un `go build` sans tampon

Les tests construisent parfois « une autre version du même programme » (`buildMark`), et un `go build` à la main n'a pas de tampon (`Built` = 0). Avec l'ordre de Q3, une build sans tampon passe après toute build qui en a un : elle utilise l'agent en place au lieu de le remplacer. Pour essayer une build locale, il faudra alors `make install` (qui tamponne) ou `errand agents restart`.

➡️ **Recommandé :** oui. Une build sans tampon passe après les autres, et le `Makefile` tamponne aussi ses `go build` (`make build`, `make install`).

**Réponse :**

ok pour toujours mettre un tampon. Est-ce que que pour les tests, il faudrait mettre en place une variable d'environnement pour faire tourner des agents en parallèle du errand de base dans d'autres répertoires, sockets, etc... et offrir la possibilité de tout nettoyer à la fin des tests ? Ça risque de compliquer beaucoup de choses, donc je ne sais pas si c'est la meilleure solution.

---
