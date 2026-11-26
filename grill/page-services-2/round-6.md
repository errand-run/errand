# Un errand à part, pour essayer : tour 6

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris autre chose (réponse, objection, commentaire) sous n'importe quelle question, puis Envoyer.

Ce qu'un errand partage aujourd'hui, et que `ERRAND_CACHE_DIR` seul ne sépare pas :

| où | quoi | risque avec un essai |
|---|---|---|
| ici | `~/.cache/errand` : agents, baux, page, envs des services, registre des machines | séparé par `ERRAND_CACHE_DIR` |
| ici | `~/.config/errand` : autorisations, sources de développement | partagé |
| ici | la file de la machine, `$XDG_RUNTIME_DIR/errand-queue-…` | partagé (voir Q2) |
| ici | `/tmp/errand-<uid>` (sockets courtes), `/tmp/errand-ssh-<uid>` (connexions ssh gardées) | partagé, peu grave |
| ici | les unités launchd ou systemd (`dev.errand.page`, `dev.errand.lease.…`) | collision |
| ailleurs | `~/.cache/errand` de lmo : son agent, ses baux, le nom stable `bin/errand` | **un essai remplace l'agent de lmo et le nom stable (Boucles)** |
| projets | `errand-outputs/`, `errand-cache/` | partagé (voir Q4) |

---

## Q1 : une seule variable, `ERRAND_INSTANCE=NOM`, qui suffixe tout

`ERRAND_INSTANCE=try` suffit à séparer tout ce qui appartient à errand :

- `~/.cache/errand-try` et `~/.config/errand-try` ;
- `/tmp/errand-<uid>-try` ;
- les unités `dev.errand-try.page`, `dev.errand-try.lease.…` ;
- la page sur un port libre (jamais 20501), avec son cookie à elle.

Un nom fait de lettres, de chiffres et de `-`. Les tests de bout en bout s'en servent aussi, au lieu de leur trio de variables. `ERRAND_CACHE_DIR` reste pour déplacer le répertoire d'un errand (un `$SCRATCH` sur un calculateur), pas pour faire un second errand.

➡️ **Recommandé :** oui. `ERRAND_INSTANCE` suffixe tous les noms d'errand, ici et ailleurs (Q3).

**Réponse :**

Oui, ERRAND_CACHE_DIR reste, mais il faudrait idéalement y avoir un sous-dossier par instance. On pourrait imaginer que l'instance principal soit main.

---

## Q2 : la file de la machine reste commune

Une machine n'a qu'un GPU et qu'une mémoire. Un essai qui prend la carte doit le dire à la même file que tout le reste, sinon deux errands la donneraient chacun de leur côté. Les réservations sont faites par processus ; deux instances ne se confondent donc pas.

Les tests de bout en bout gardent leur propre file (`XDG_RUNTIME_DIR` d'un dossier temporaire), comme aujourd'hui : on y simule des machines.

➡️ **Recommandé :** oui. La file reste commune à toutes les instances d'une machine ; seuls les tests en ont une à eux.

**Réponse :**

---

## Q3 : l'instance va avec errand sur les autres machines

Un essai qui atteint lmo y utilise `~/.cache/errand-try` : son agent, ses baux, son propre nom stable `~/.cache/errand-try/bin/errand`. Il ne touche jamais à l'agent de lmo, ni au `~/.cache/errand/bin/errand` que force la clé de Boucles, ni à ses autorisations. La file de lmo reste commune (Q2). Un `Ssh( cache = … )` déclaré reçoit le même suffixe.

➡️ **Recommandé :** oui. L'instance suit errand partout.

**Réponse :**

---

## Q4 : les projets ne changent pas

`errand-outputs/` et `errand-cache/` sont au projet, pas à errand : un run fait par un essai reste un run du projet. Son `result.json` dit l'instance (`"instance": "try"`), et la page d'errand la montre. Sinon il faudrait des `errand-outputs-try/` qui se perdent.

➡️ **Recommandé :** oui, rien de suffixé dans les projets ; l'instance est dans le dossier du run.

**Réponse :**

---

## Q5 : le voir, et nettoyer

- **Une ligne** dès qu'une commande tourne dans une autre instance, ou avec un `ERRAND_CACHE_DIR` qui n'est pas le répertoire habituel : `errand [try]: ~/.cache/errand-try`. Elle est sur stderr, pour ne gêner aucun script, et l'en-tête de la page la montre aussi.
- **`errand instances`** liste les instances trouvées ici (`~/.cache/errand-*`) avec leur taille et leurs agents en cours.
- **`errand instances remove try`** arrête ses agents, sa page et ses unités, ici et sur les machines qu'elle a atteintes, puis supprime ses répertoires après les avoir montrés et demandé confirmation.

➡️ **Recommandé :** oui à la ligne, à `errand instances` et à `remove`.

**Réponse :**

---
