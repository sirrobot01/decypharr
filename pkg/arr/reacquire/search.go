package reacquire

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

func (handler *arrHandler) searchBindings(
	ctx context.Context,
	instance arr.Arr,
	job *Job,
	bindings []Binding,
	progress JobProgress,
) (Status, error) {
	if err := progress.Update(StatusSearching, nil); err != nil {
		return "", err
	}

	mutation, err := searchMutation(instance, bindings)
	if err != nil {
		return "", err
	}
	mutation, err = ensureMutationIntent(job, progress, StatusSearching, mutation)
	if err != nil {
		return "", err
	}
	if mutation.State == MutationConfirmed {
		return StatusWaitingForGrab, nil
	}
	if mutation.Attempts > 0 {
		command, found, err := handler.reconcileCommandMutation(ctx, instance, mutation)
		if err != nil {
			return "", unavailableMutationReconciliation(mutation, err)
		}
		if found {
			if err := confirmMutation(job, progress, StatusSearching, mutation, command.ID); err != nil {
				return "", err
			}
			return StatusWaitingForGrab, nil
		}
		if err := mutationRedispatchError(mutation); err != nil {
			return "", err
		}
	}
	mutation, err = recordMutationAttempt(job, progress, StatusSearching, mutation)
	if err != nil {
		return "", err
	}
	command, err := handler.dispatchSearchCommand(ctx, instance, mutation)
	if err != nil {
		if !errors.Is(err, arr.ErrMutationOutcomeUnknown) {
			return "", err
		}
		receipt, found, reconcileErr := handler.reconcileCommandMutation(ctx, instance, mutation)
		if reconcileErr == nil && found {
			if err := confirmMutation(job, progress, StatusSearching, mutation, receipt.ID); err != nil {
				return "", err
			}
			return StatusWaitingForGrab, nil
		}
		return "", unresolvedMutation(mutation, err, reconcileErr)
	}
	if err := confirmMutation(job, progress, StatusSearching, mutation, command.ID); err != nil {
		return "", err
	}
	return StatusWaitingForGrab, nil
}

func searchMutation(instance arr.Arr, bindings []Binding) (Mutation, error) {
	switch instance.Type {
	case arr.Sonarr:
		episodeIDs, seriesID, seasonNumber := sonarrTargets(bindings)
		if len(episodeIDs) > 0 {
			return Mutation{
				Key:         mutationKey(MutationEpisodeSearch, idListKey(episodeIDs)),
				Kind:        MutationEpisodeSearch,
				CommandName: "EpisodeSearch",
				EpisodeIDs:  episodeIDs,
			}, nil
		}
		return Mutation{
			Key:          mutationKey(MutationSeasonSearch, strconv.Itoa(seriesID), strconv.Itoa(seasonNumber)),
			Kind:         MutationSeasonSearch,
			CommandName:  "SeasonSearch",
			SeriesID:     seriesID,
			SeasonNumber: seasonNumber,
		}, nil
	case arr.Radarr:
		movieIDs := movieTargets(bindings)
		return Mutation{
			Key:         mutationKey(MutationMovieSearch, idListKey(movieIDs)),
			Kind:        MutationMovieSearch,
			CommandName: "MoviesSearch",
			MovieIDs:    movieIDs,
		}, nil
	default:
		return Mutation{}, fmt.Errorf("search unsupported for arr type %q", instance.Type)
	}
}

func (handler *arrHandler) dispatchSearchCommand(ctx context.Context, instance arr.Arr, mutation Mutation) (arr.Command, error) {
	switch mutation.Kind {
	case MutationEpisodeSearch:
		return handler.arrs.SearchEpisodes(ctx, instance.Name, mutation.EpisodeIDs)
	case MutationSeasonSearch:
		return handler.arrs.SearchSeason(ctx, instance.Name, mutation.SeriesID, mutation.SeasonNumber)
	case MutationMovieSearch:
		return handler.arrs.SearchMovies(ctx, instance.Name, mutation.MovieIDs)
	default:
		return arr.Command{}, fmt.Errorf("unsupported Arr command mutation %q", mutation.Kind)
	}
}

func (handler *arrHandler) reconcileCommandMutation(ctx context.Context, instance arr.Arr, mutation Mutation) (arr.Command, bool, error) {
	commands, err := handler.arrs.Commands(ctx, instance.Name)
	if err != nil {
		return arr.Command{}, false, fmt.Errorf("reconcile Arr command: %w", err)
	}
	command, found := findCommandReceipt(commands, mutation)
	return command, found, nil
}
